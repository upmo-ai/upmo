package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

// Adapter talks OpenAI-compatible /v1/chat/completions. Production uses
// netguard; tests inject Client.
type Adapter struct {
	Client *http.Client
}

func (a Adapter) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Transport: (&netguard.Guard{}).Transport()}
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
	res, err := a.client().Do(req)
	if err != nil {
		return "", domain.Usage{}, classForErr(err), err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", domain.Usage{}, domain.ClassTransient, err
	}
	if res.StatusCode == http.StatusTooManyRequests {
		return "", domain.Usage{}, domain.ClassRateLimit, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", domain.Usage{}, domain.ClassCredential, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	if res.StatusCode == http.StatusNotFound {
		return "", domain.Usage{}, domain.ClassModelAccess, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	if res.StatusCode >= 500 {
		return "", domain.Usage{}, domain.ClassTransient, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: provider %d", res.StatusCode)
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

func classForErr(err error) domain.FailureClass {
	if err == nil {
		return domain.ClassOK
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return domain.ClassTimeout
	}
	return domain.ClassTransient
}
