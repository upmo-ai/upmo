package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

// Adapter talks OpenAI-compatible /v1/chat/completions. Production dials
// through netguard; tests inject Client.
type Adapter struct {
	Client *http.Client
	// Lookup resolves host names for the guarded clients. Nil uses the system
	// resolver. Tests set it to re-point a name.
	Lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// The guarded clients. Every connection resolves the host and refuses the
// dial when any address is one the account's plan forbids, then dials the
// vetted address, so a name cannot be re-pointed after a URL was saved. Only
// a local account may reach a private or loopback address; none may reach a
// link-local or metadata address. A redirect is never followed: the answer
// is the redirect itself, so a key is not sent on to another place.
var (
	strictClient = guardedClient(false, nil)
	localClient  = guardedClient(true, nil)
)

func guardedClient(local bool, lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)) *http.Client {
	g := &netguard.Guard{LocalNetwork: local, Lookup: lookup}
	return &http.Client{
		Transport: g.Transport(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (a Adapter) client(attempt app.Attempt) *http.Client {
	if a.Client != nil {
		return a.Client
	}
	local := attempt.PlanKind == domain.PlanLocal
	if a.Lookup != nil {
		return guardedClient(local, a.Lookup)
	}
	if local {
		return localClient
	}
	return strictClient
}

// Probe asks for the model list, the cheapest call that needs the key. It
// returns the failure class of the answer.
func (a Adapter) Probe(ctx context.Context, attempt app.Attempt) (domain.FailureClass, error) {
	base := strings.TrimRight(attempt.BaseURL, "/")
	if base == "" {
		return domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return domain.ClassCaller, err
	}
	if attempt.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+attempt.Secret)
	}
	res, err := a.client(attempt).Do(req)
	if err != nil {
		return classForErr(err), err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	return statusClass(res.StatusCode)
}

func (a Adapter) Decide(ctx context.Context, attempt app.Attempt, req domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error) {
	prompt := req.Prompt
	if req.Kind == domain.DecisionYesNo {
		prompt += "\nAnswer with yes or no."
	} else {
		prompt += "\nAnswer with one of: " + strings.Join(req.Options, ", ")
	}
	text, _, class, err := a.complete(ctx, attempt, domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: prompt}},
	})
	if err != nil {
		return domain.DecideResult{}, domain.Usage{}, class, err
	}
	switch req.Kind {
	case domain.DecisionYesNo:
		yes, err := domain.ParseYesNo(strings.ToLower(strings.TrimSpace(text)))
		if err != nil {
			return domain.DecideResult{}, domain.Usage{}, domain.ClassCaller, err
		}
		return domain.DecideResult{YesNo: yes}, domain.Usage{}, domain.ClassOK, nil
	case domain.DecisionChoice:
		got, err := domain.ParseChoice(strings.TrimSpace(text), req.Options)
		if err != nil {
			return domain.DecideResult{}, domain.Usage{}, domain.ClassCaller, err
		}
		return domain.DecideResult{Choice: got}, domain.Usage{}, domain.ClassOK, nil
	default:
		return domain.DecideResult{}, domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: decide kind %q", req.Kind)
	}
}

func (a Adapter) Chat(ctx context.Context, attempt app.Attempt, req domain.Request) (string, domain.Usage, domain.FailureClass, error) {
	return a.complete(ctx, attempt, req)
}

type cacheCtl struct {
	Type string `json:"type"`
}

type chatToolFn struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatTool struct {
	Type         string     `json:"type"`
	Function     chatToolFn `json:"function"`
	CacheControl *cacheCtl  `json:"cache_control,omitempty"`
}

type chatMsg struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type systemBlock struct {
	Content      string    `json:"content"`
	CacheControl *cacheCtl `json:"cache_control,omitempty"`
}

// chatBody is the OpenAI-compat payload. Field order is tools, system,
// messages so the cache prefix sits in front of the turn.
type chatBody struct {
	Model    string     `json:"model"`
	Tools    []chatTool `json:"tools,omitempty"`
	System   any        `json:"system,omitempty"`
	Messages []chatMsg  `json:"messages"`
}

func encodeChat(model string, req domain.Request) ([]byte, error) {
	breakTools := false
	breakSystem := false
	for _, mark := range req.Cache {
		if mark == domain.BreakTools {
			breakTools = true
		}
		if mark == domain.BreakSystem {
			breakSystem = true
		}
	}
	var system any
	if req.System != "" {
		if breakSystem {
			system = systemBlock{Content: req.System, CacheControl: &cacheCtl{Type: "ephemeral"}}
		} else {
			system = req.System
		}
	}
	body := chatBody{Model: model, System: system}
	for i, tool := range req.Tools {
		item := chatTool{
			Type: "function",
			Function: chatToolFn{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Schema,
			},
		}
		if len(bytes.TrimSpace(item.Function.Parameters)) == 0 {
			item.Function.Parameters = json.RawMessage(`{}`)
		}
		if breakTools && i == len(req.Tools)-1 {
			item.CacheControl = &cacheCtl{Type: "ephemeral"}
		}
		body.Tools = append(body.Tools, item)
	}
	for _, msg := range req.Messages {
		body.Messages = append(body.Messages, chatMsg{
			Role:       string(msg.Role),
			Content:    msg.Text,
			ToolCallID: msg.ToolID,
		})
	}
	return json.Marshal(body)
}

func (a Adapter) complete(ctx context.Context, attempt app.Attempt, built domain.Request) (string, domain.Usage, domain.FailureClass, error) {
	base := strings.TrimRight(attempt.BaseURL, "/")
	if base == "" {
		return "", domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	body, err := encodeChat(attempt.Model, built)
	if err != nil {
		return "", domain.Usage{}, domain.ClassCaller, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", domain.Usage{}, domain.ClassCaller, err
	}
	req.Header.Set("Content-Type", "application/json")
	if attempt.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+attempt.Secret)
	}
	res, err := a.client(attempt).Do(req)
	if err != nil {
		return "", domain.Usage{}, classForErr(err), err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", domain.Usage{}, domain.ClassTransient, err
	}
	if class, err := statusClass(res.StatusCode); err != nil {
		return "", domain.Usage{}, class, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: provider body")
	}
	usage := domain.Usage{}
	if parsed.Usage != nil {
		usage.Arrived = true
		usage.Input = parsed.Usage.PromptTokens
		usage.Output = parsed.Usage.CompletionTokens
		if parsed.Usage.PromptTokensDetails != nil {
			usage.CacheRead = parsed.Usage.PromptTokensDetails.CachedTokens
		}
	}
	return parsed.Choices[0].Message.Content, usage, domain.ClassOK, nil
}

func (a Adapter) Embed(ctx context.Context, attempt app.Attempt, inputs []string) ([][]float32, domain.Usage, domain.FailureClass, error) {
	if err := domain.CheckEmbedInputs(inputs); err != nil {
		return nil, domain.Usage{}, domain.ClassCaller, err
	}
	base := strings.TrimRight(attempt.BaseURL, "/")
	if base == "" {
		return nil, domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{Model: attempt.Model, Input: inputs})
	if err != nil {
		return nil, domain.Usage{}, domain.ClassCaller, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, domain.Usage{}, domain.ClassCaller, err
	}
	req.Header.Set("Content-Type", "application/json")
	if attempt.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+attempt.Secret)
	}
	res, err := a.client(attempt).Do(req)
	if err != nil {
		return nil, domain.Usage{}, classForErr(err), err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, domain.Usage{}, domain.ClassTransient, err
	}
	if class, err := statusClass(res.StatusCode); err != nil {
		return nil, domain.Usage{}, class, err
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: provider body")
	}
	out := make([][]float32, len(inputs))
	seen := make([]bool, len(inputs))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(inputs) || seen[item.Index] {
			return nil, domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: provider body")
		}
		seen[item.Index] = true
		out[item.Index] = item.Embedding
	}
	for _, ok := range seen {
		if !ok {
			return nil, domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: provider body")
		}
	}
	usage := domain.Usage{}
	if parsed.Usage != nil {
		usage.Arrived = true
		usage.Input = parsed.Usage.PromptTokens
	}
	return out, usage, domain.ClassOK, nil
}

func statusClass(code int) (domain.FailureClass, error) {
	switch {
	case code == http.StatusTooManyRequests:
		return domain.ClassRateLimit, fmt.Errorf("gateway: provider %d", code)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return domain.ClassCredential, fmt.Errorf("gateway: provider %d", code)
	case code == http.StatusNotFound:
		return domain.ClassModelAccess, fmt.Errorf("gateway: provider %d", code)
	case code >= 500:
		return domain.ClassTransient, fmt.Errorf("gateway: provider %d", code)
	case code < 200 || code >= 300:
		return domain.ClassCaller, fmt.Errorf("gateway: provider %d", code)
	default:
		return domain.ClassOK, nil
	}
}

func classForErr(err error) domain.FailureClass {
	if err == nil {
		return domain.ClassOK
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return domain.ClassTimeout
	}
	return domain.ClassTransient
}
