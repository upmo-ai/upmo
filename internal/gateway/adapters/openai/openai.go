package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/wire"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/kernel"
)

// Adapter talks OpenAI-compatible chat completions. The paths come from the
// vendor the account's base URL belongs to (domain.CompatPaths); a host that
// is no known vendor's gets /v1/chat/completions. Production dials through
// netguard; tests inject Client.
type Adapter struct {
	Client *http.Client
	// Lookup resolves host names for the guarded clients. Nil uses the system
	// resolver. Tests set it to re-point a name.
	Lookup wire.Lookup
}

func (a Adapter) client(attempt app.Attempt) *http.Client {
	return wire.Client(a.Client, a.Lookup, attempt.PlanKind)
}

var (
	statusClass = wire.StatusClass
	classForErr = wire.ClassForErr
)

// target splits an attempt into the account's base URL and the paths of the
// vendor that base URL is on. ok is false when the account has no base URL.
func target(attempt app.Attempt) (base string, paths domain.Vendor, ok bool) {
	base = strings.TrimRight(attempt.BaseURL, "/")
	if base == "" {
		return "", domain.Vendor{}, false
	}
	return base, domain.CompatPaths(base), true
}

func authorize(req *http.Request, style domain.AuthStyle, secret string) {
	if secret == "" {
		return
	}
	if style == domain.AuthAPIKey {
		req.Header.Set("api-key", secret)
		return
	}
	req.Header.Set("Authorization", "Bearer "+secret)
}

// Probe asks for the model list, the cheapest call that needs the key. It
// returns the discovered models and the failure class of the answer. A
// malformed or oversized list keeps ClassOK when the auth call succeeded and
// returns nil for the models.
func (a Adapter) Probe(ctx context.Context, attempt app.Attempt) ([]domain.SeenModel, domain.FailureClass, error) {
	base, paths, ok := target(attempt)
	if !ok {
		return nil, domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+paths.ModelsPath, nil)
	if err != nil {
		return nil, domain.ClassCaller, err
	}
	authorize(req, paths.AuthStyle, attempt.Secret)
	res, err := a.client(attempt).Do(req)
	if err != nil {
		return nil, classForErr(err), err
	}
	defer func() { _ = res.Body.Close() }()
	if class, err := statusClass(res.StatusCode); err != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		return nil, class, err
	}
	raw, err := wire.ReadModelList(res.Body)
	if err != nil {
		return nil, domain.ClassOK, nil
	}
	models, err := parseModels(raw)
	if err != nil {
		return nil, domain.ClassOK, nil
	}
	return models, domain.ClassOK, nil
}

type openAIArchitecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

type openAIModelItem struct {
	Object        string              `json:"object"`
	ID            string              `json:"id"`
	ContextLength *int                `json:"context_length"`
	Architecture  *openAIArchitecture `json:"architecture"`
	Pricing       map[string]*string  `json:"pricing"`
}

func parseModels(raw []byte) ([]domain.SeenModel, error) {
	var parsed struct {
		Object string            `json:"object"`
		Data   []openAIModelItem `json:"data"`
	}
	if err := wire.DecodeStrictObject(raw, &parsed); err != nil {
		return nil, err
	}
	if parsed.Object != "" && parsed.Object != "list" {
		return nil, fmt.Errorf("gateway: unexpected model list object")
	}
	if parsed.Data == nil {
		return nil, fmt.Errorf("gateway: missing model data")
	}
	// Refuse lists exceeding MaxSeenModels rather than truncating so callers
	// never store a partial model list.
	if len(parsed.Data) > domain.MaxSeenModels {
		return nil, fmt.Errorf("gateway: too many models")
	}
	out := make([]domain.SeenModel, 0, len(parsed.Data))
	seen := make(map[string]bool, len(parsed.Data))
	for _, item := range parsed.Data {
		if item.Object != "" && item.Object != "model" {
			return nil, fmt.Errorf("gateway: unexpected model entry object")
		}
		// Gemini's OpenAI-compatible list prefixes model ids with "models/".
		rawID := strings.TrimPrefix(strings.TrimSpace(item.ID), "models/")
		id, err := domain.CleanModelID(rawID)
		if err != nil {
			return nil, err
		}
		var ctxLen int
		if item.ContextLength != nil {
			if *item.ContextLength < 0 {
				return nil, fmt.Errorf("gateway: invalid context length")
			}
			ctxLen = *item.ContextLength
		}
		pricing, err := cleanPricing(item.Pricing)
		if err != nil {
			return nil, err
		}
		modalities, err := cleanModalities(item.Architecture)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, domain.SeenModel{
			ID:            id,
			ContextLength: ctxLen,
			Pricing:       pricing,
			Modalities:    modalities,
		})
	}
	return out, nil
}

func cleanPricing(in map[string]*string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(*v)
		if key == "" || !domain.ValidModelText(key) || !domain.ValidModelText(val) {
			return nil, fmt.Errorf("gateway: invalid pricing")
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func cleanModalities(arch *openAIArchitecture) (*domain.ModelModalities, error) {
	if arch == nil {
		return nil, nil
	}
	cleanList := func(items []string) ([]string, error) {
		if len(items) == 0 {
			return nil, nil
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			s := strings.TrimSpace(item)
			if s == "" || !domain.ValidModelText(s) {
				return nil, fmt.Errorf("gateway: invalid modality")
			}
			out = append(out, s)
		}
		return out, nil
	}
	in, err := cleanList(arch.InputModalities)
	if err != nil {
		return nil, err
	}
	out, err := cleanList(arch.OutputModalities)
	if err != nil {
		return nil, err
	}
	if len(in) == 0 && len(out) == 0 && strings.TrimSpace(arch.Modality) != "" {
		if !domain.ValidModelText(arch.Modality) {
			return nil, fmt.Errorf("gateway: invalid modality")
		}
		if lhs, rhs, ok := strings.Cut(arch.Modality, "->"); ok {
			if in, err = cleanList(strings.Split(lhs, "+")); err != nil {
				return nil, err
			}
			if out, err = cleanList(strings.Split(rhs, "+")); err != nil {
				return nil, err
			}
		}
	}
	if len(in) == 0 && len(out) == 0 {
		return nil, nil
	}
	return &domain.ModelModalities{Input: in, Output: out}, nil
}

func (a Adapter) Decide(ctx context.Context, attempt app.Attempt, req domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error) {
	prompt := req.Prompt
	if req.Kind == domain.DecisionYesNo {
		prompt += "\nAnswer with yes or no."
	} else {
		prompt += "\nAnswer with one of: " + strings.Join(req.Options, ", ")
	}
	res, class, err := a.complete(ctx, attempt, domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: prompt}},
	})
	if err != nil {
		return domain.DecideResult{}, res.Usage, class, err
	}
	switch req.Kind {
	case domain.DecisionYesNo:
		yes, err := domain.ParseYesNo(strings.ToLower(strings.TrimSpace(res.Text)))
		if err != nil {
			return domain.DecideResult{}, res.Usage, domain.ClassCaller, err
		}
		return domain.DecideResult{YesNo: yes}, res.Usage, domain.ClassOK, nil
	case domain.DecisionChoice:
		got, err := domain.ParseChoice(strings.TrimSpace(res.Text), req.Options)
		if err != nil {
			return domain.DecideResult{}, res.Usage, domain.ClassCaller, err
		}
		return domain.DecideResult{Choice: got}, res.Usage, domain.ClassOK, nil
	default:
		return domain.DecideResult{}, res.Usage, domain.ClassCaller, fmt.Errorf("gateway: decide kind %q", req.Kind)
	}
}

func (a Adapter) Chat(ctx context.Context, attempt app.Attempt, req domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	return a.complete(ctx, attempt, req)
}

// ChatStream streams a chat reply, handing each text delta to emit in order.
// When emit fails the caller is gone; the read stops and the call returns.
func (a Adapter) ChatStream(ctx context.Context, attempt app.Attempt, req domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	res, class, err := a.ChatStreamTools(ctx, attempt, req, app.StreamEvents{Text: emit})
	return res.Usage, class, err
}

// ChatStreamTools streams text and tool-call deltas and returns the
// accumulated ChatResult.
func (a Adapter) ChatStreamTools(ctx context.Context, attempt app.Attempt, req domain.Request, events app.StreamEvents) (domain.ChatResult, domain.FailureClass, error) {
	base, paths, ok := target(attempt)
	if !ok {
		return domain.ChatResult{}, domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	body, err := encodeStream(attempt.Model, req)
	if err != nil {
		return domain.ChatResult{}, domain.ClassCaller, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+paths.ChatPath, bytes.NewReader(body))
	if err != nil {
		return domain.ChatResult{}, domain.ClassCaller, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	authorize(httpReq, paths.AuthStyle, attempt.Secret)
	res, err := a.client(attempt).Do(httpReq)
	if err != nil {
		return domain.ChatResult{}, classForErr(err), err
	}
	defer func() { _ = res.Body.Close() }()
	if class, err := statusClass(res.StatusCode); err != nil {
		return domain.ChatResult{}, class, err
	}
	return readStreamTools(ctx, attempt.Model, res.Body, events)
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

type googleExtra struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

type extraContent struct {
	Google *googleExtra `json:"google,omitempty"`
}

type chatToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatToolCall struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Function     chatToolCallFn `json:"function"`
	ExtraContent *extraContent  `json:"extra_content,omitempty"`
}

type chatMsg struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type systemBlock struct {
	Content      string    `json:"content"`
	CacheControl *cacheCtl `json:"cache_control,omitempty"`
}

// chatBody is the OpenAI-compat payload. Field order is tools, system,
// messages so the cache prefix sits in front of the turn.
type chatBody struct {
	Model      string     `json:"model"`
	Tools      []chatTool `json:"tools,omitempty"`
	System     any        `json:"system,omitempty"`
	Messages   []chatMsg  `json:"messages"`
	ToolChoice any        `json:"tool_choice,omitempty"`
	MaxTokens  int        `json:"max_tokens,omitempty"`
}

func encodeChat(model string, req domain.Request) ([]byte, error) {
	return json.Marshal(buildChat(model, req))
}

func buildChat(model string, req domain.Request) chatBody {
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
	body := chatBody{
		Model:      model,
		System:     system,
		ToolChoice: encodeToolChoice(req.ToolChoice),
		MaxTokens:  req.MaxOutputTokens,
	}
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
		body.Messages = append(body.Messages, encodeMessage(msg))
	}
	return body
}

func encodeMessage(msg domain.Message) chatMsg {
	switch msg.Role {
	case domain.RoleAssistant:
		if len(msg.ToolCalls) == 0 {
			text := msg.Text
			return chatMsg{Role: "assistant", Content: &text}
		}
		var content *string
		if msg.Text != "" {
			text := msg.Text
			content = &text
		}
		calls := make([]chatToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			args := strings.TrimSpace(string(tc.Arguments))
			if args == "" {
				args = "{}"
			}
			call := chatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: chatToolCallFn{
					Name:      tc.Name,
					Arguments: args,
				},
			}
			if tc.ThoughtSignature != "" {
				call.ExtraContent = &extraContent{
					Google: &googleExtra{ThoughtSignature: tc.ThoughtSignature},
				}
			}
			calls = append(calls, call)
		}
		return chatMsg{Role: "assistant", Content: content, ToolCalls: calls}
	case domain.RoleTool:
		text := msg.Text
		if msg.IsError {
			if text == "" {
				text = "Error: tool execution failed"
			} else {
				text = "Error: " + text
			}
		}
		return chatMsg{Role: "tool", Content: &text, ToolCallID: msg.ToolID}
	default:
		text := msg.Text
		return chatMsg{Role: "user", Content: &text}
	}
}

func encodeToolChoice(tc domain.ToolChoice) any {
	if tc.IsZero() {
		return nil
	}
	switch tc.Mode {
	case domain.ToolChoiceAuto:
		return "auto"
	case domain.ToolChoiceAny:
		return "required"
	case domain.ToolChoiceNone:
		return "none"
	case domain.ToolChoiceTool:
		return map[string]any{
			"type":     "function",
			"function": map[string]string{"name": tc.Name},
		}
	default:
		return nil
	}
}

// streamBody is the streaming payload: chatBody plus the stream flags, which
// sit after the cache prefix.
type streamBody struct {
	chatBody
	Stream        bool          `json:"stream"`
	StreamOptions streamOptions `json:"stream_options"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func encodeStream(model string, req domain.Request) ([]byte, error) {
	return json.Marshal(streamBody{
		chatBody:      buildChat(model, req),
		Stream:        true,
		StreamOptions: streamOptions{IncludeUsage: true},
	})
}

type usagePayload struct {
	PromptTokens         int `json:"prompt_tokens"`
	CompletionTokens     int `json:"completion_tokens"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	Cost           json.RawMessage `json:"cost"`
	CostInUSDTicks json.RawMessage `json:"cost_in_usd_ticks"`
	IsBYOK         bool            `json:"is_byok"`
	CostDetails    *struct {
		UpstreamInferenceCost json.RawMessage `json:"upstream_inference_cost"`
	} `json:"cost_details"`
}

func (u usagePayload) toDomain() domain.Usage {
	prompt := max(0, u.PromptTokens)
	completion := max(0, u.CompletionTokens)
	cached := max(0, u.PromptCacheHitTokens)
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > cached {
		cached = u.PromptTokensDetails.CachedTokens
	}
	if cached > prompt {
		cached = prompt
	}
	var upstreamCost json.RawMessage
	if u.CostDetails != nil {
		upstreamCost = u.CostDetails.UpstreamInferenceCost
	}
	out := domain.Usage{
		Arrived:              true,
		Input:                prompt - cached,
		Output:               completion,
		CacheRead:            cached,
		ReportedCostMicroUSD: parseReportedCost(u.Cost, u.CostInUSDTicks, upstreamCost, u.IsBYOK),
	}
	if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens > 0 {
		out.Reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

func parseReportedCost(costRaw, ticksRaw, upstreamRaw json.RawMessage, isBYOK bool) *int64 {
	if isBYOK {
		micro, ok := domain.ParseDecimalMicroUSD(costRaw, kernel.MicroUSDPerDollar)
		upstream, upstreamOK := domain.ParseDecimalMicroUSD(upstreamRaw, kernel.MicroUSDPerDollar)
		if !ok || !upstreamOK || micro < 0 || micro > domain.MaxMicroUSD ||
			upstream < 0 || upstream > domain.MaxMicroUSD || micro > domain.MaxMicroUSD-upstream {
			return nil
		}
		total := micro + upstream
		return &total
	}
	if trimmed := bytes.TrimSpace(costRaw); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if trimmed[0] == '{' {
			var obj struct {
				TotalCost json.RawMessage `json:"total_cost"`
			}
			if err := json.Unmarshal(trimmed, &obj); err == nil {
				if micro, ok := domain.ParseDecimalMicroUSD(obj.TotalCost, kernel.MicroUSDPerDollar); ok && micro >= 0 && micro <= domain.MaxMicroUSD {
					return &micro
				}
			}
		} else if micro, ok := domain.ParseDecimalMicroUSD(trimmed, kernel.MicroUSDPerDollar); ok && micro >= 0 && micro <= domain.MaxMicroUSD {
			return &micro
		}
	}
	if trimmed := bytes.TrimSpace(ticksRaw); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if micro, ok := domain.ParseTicksMicroUSD(trimmed); ok && micro >= 0 && micro <= domain.MaxMicroUSD {
			return &micro
		}
	}
	return nil
}

type streamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	ExtraContent *extraContent `json:"extra_content"`
}

// streamChunk is one SSE data payload of a streamed completion.
type streamChunk struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content      string           `json:"content"`
			ToolCalls    []streamToolCall `json:"tool_calls"`
			ExtraContent *extraContent    `json:"extra_content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *usagePayload `json:"usage"`
}

type streamToolAcc struct {
	id               string
	name             string
	args             strings.Builder
	thoughtSignature string
}

type streamState struct {
	model      string
	usage      domain.Usage
	text       strings.Builder
	stopReason string
	byIndex    map[int]int
	tools      []streamToolAcc
}

func (s *streamState) result() domain.ChatResult {
	var calls []domain.ToolCall
	if len(s.tools) > 0 {
		calls = make([]domain.ToolCall, len(s.tools))
		for i, acc := range s.tools {
			id := acc.id
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			args := strings.TrimSpace(acc.args.String())
			if args == "" {
				args = "{}"
			}
			calls[i] = domain.ToolCall{
				ID:               id,
				Name:             acc.name,
				Arguments:        json.RawMessage(args),
				ThoughtSignature: acc.thoughtSignature,
			}
		}
	}
	return domain.ChatResult{
		Text:       s.text.String(),
		ToolCalls:  calls,
		StopReason: s.stopReason,
		Usage:      s.usage,
		Model:      s.model,
	}
}

func readStream(ctx context.Context, body io.Reader, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	res, class, err := readStreamTools(ctx, "", body, app.StreamEvents{Text: emit})
	return res.Usage, class, err
}

// readStreamTools decodes the SSE events of a streamed completion. The data:
// lines of one event are one JSON chunk, dispatched at the blank line that
// ends the event (a pending event at the end of the body dispatches too);
// comment lines and other fields are ignored. Text and tool-call deltas go
// through events in order, then the [DONE] sentinel ends the stream. A stream
// that ends any other way was cut short.
func readStreamTools(ctx context.Context, model string, body io.Reader, events app.StreamEvents) (domain.ChatResult, domain.FailureClass, error) {
	in := bufio.NewReader(body)
	state := &streamState{
		model:   model,
		byIndex: map[int]int{},
	}
	var data []string
	// event runs the collected data lines as one chunk. done reports whether
	// the stream ended there.
	event := func() (class domain.FailureClass, err error, done bool) {
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "[DONE]" {
			return domain.ClassOK, nil, true
		}
		if class, err = oneChunk(payload, state, events); class != domain.ClassOK || err != nil {
			return class, err, true
		}
		return domain.ClassOK, nil, false
	}
	for {
		line, rerr := in.ReadString('\n')
		if text := strings.TrimSpace(line); text != "" {
			if payload, ok := strings.CutPrefix(text, "data:"); ok {
				data = append(data, strings.TrimSpace(payload))
			}
		} else if len(data) > 0 {
			if class, err, done := event(); done {
				return state.result(), class, err
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				if len(data) > 0 {
					if class, err, done := event(); done {
						return state.result(), class, err
					}
				}
				if ctx.Err() != nil {
					return state.result(), domain.ClassCanceled, ctx.Err()
				}
				return state.result(), domain.ClassTransient, errors.New("gateway: stream ended without [DONE]")
			}
			if ctx.Err() != nil {
				return state.result(), domain.ClassCanceled, rerr
			}
			return state.result(), classForErr(rerr), rerr
		}
	}
}

func oneChunk(payload string, state *streamState, events app.StreamEvents) (domain.FailureClass, error) {
	var chunk streamChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return domain.ClassCaller, fmt.Errorf("gateway: provider body")
	}
	if chunk.Usage != nil {
		state.usage = chunk.Usage.toDomain()
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != "" {
			state.stopReason = choice.FinishReason
		}
		if choice.Delta.Content != "" {
			state.text.WriteString(choice.Delta.Content)
			if events.Text != nil {
				if err := events.Text(choice.Delta.Content); err != nil {
					return domain.ClassCanceled, err
				}
			}
		}
		for _, tc := range choice.Delta.ToolCalls {
			pos, ok := state.byIndex[tc.Index]
			if !ok || (tc.ID != "" && state.tools[pos].id != "" && tc.ID != state.tools[pos].id) {
				pos = len(state.tools)
				state.tools = append(state.tools, streamToolAcc{})
				state.byIndex[tc.Index] = pos
			}
			if tc.ID != "" {
				state.tools[pos].id = tc.ID
			}
			if tc.Function.Name != "" {
				state.tools[pos].name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				state.tools[pos].args.WriteString(tc.Function.Arguments)
			}
			if tc.ExtraContent != nil && tc.ExtraContent.Google != nil && tc.ExtraContent.Google.ThoughtSignature != "" {
				state.tools[pos].thoughtSignature = tc.ExtraContent.Google.ThoughtSignature
			}
			if events.ToolCall != nil && (tc.ID != "" || tc.Function.Name != "" || tc.Function.Arguments != "") {
				if err := events.ToolCall(app.ToolCallDelta{
					Index:          pos,
					ID:             tc.ID,
					Name:           tc.Function.Name,
					ArgumentsDelta: tc.Function.Arguments,
				}); err != nil {
					return domain.ClassCanceled, err
				}
			}
		}
	}
	return domain.ClassOK, nil
}

func (a Adapter) complete(ctx context.Context, attempt app.Attempt, built domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	base, paths, ok := target(attempt)
	if !ok {
		return domain.ChatResult{}, domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	body, err := encodeChat(attempt.Model, built)
	if err != nil {
		return domain.ChatResult{}, domain.ClassCaller, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+paths.ChatPath, bytes.NewReader(body))
	if err != nil {
		return domain.ChatResult{}, domain.ClassCaller, err
	}
	req.Header.Set("Content-Type", "application/json")
	authorize(req, paths.AuthStyle, attempt.Secret)
	res, err := a.client(attempt).Do(req)
	if err != nil {
		return domain.ChatResult{}, classForErr(err), err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return domain.ChatResult{}, domain.ClassTransient, err
	}
	if class, err := statusClass(res.StatusCode); err != nil {
		return domain.ChatResult{}, class, err
	}
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
					ExtraContent *extraContent `json:"extra_content"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *usagePayload `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return domain.ChatResult{}, domain.ClassCaller, fmt.Errorf("gateway: provider body")
	}
	usage := domain.Usage{}
	if parsed.Usage != nil {
		usage = parsed.Usage.toDomain()
	}
	choice := parsed.Choices[0]
	var calls []domain.ToolCall
	if len(choice.Message.ToolCalls) > 0 {
		calls = make([]domain.ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			args := strings.TrimSpace(tc.Function.Arguments)
			if args == "" {
				args = "{}"
			}
			sig := ""
			if tc.ExtraContent != nil && tc.ExtraContent.Google != nil {
				sig = tc.ExtraContent.Google.ThoughtSignature
			}
			calls[i] = domain.ToolCall{
				ID:               id,
				Name:             tc.Function.Name,
				Arguments:        json.RawMessage(args),
				ThoughtSignature: sig,
			}
		}
	}
	return domain.ChatResult{
		Text:       choice.Message.Content,
		ToolCalls:  calls,
		StopReason: choice.FinishReason,
		Usage:      usage,
		Model:      attempt.Model,
	}, domain.ClassOK, nil
}

func (a Adapter) Embed(ctx context.Context, attempt app.Attempt, inputs []string) ([][]float32, domain.Usage, domain.FailureClass, error) {
	if err := domain.CheckEmbedInputs(inputs); err != nil {
		return nil, domain.Usage{}, domain.ClassCaller, err
	}
	base, paths, ok := target(attempt)
	if !ok {
		return nil, domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	if paths.EmbedPath == "" {
		return nil, domain.Usage{}, domain.ClassModelAccess, domain.ErrCapability
	}
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{Model: attempt.Model, Input: inputs})
	if err != nil {
		return nil, domain.Usage{}, domain.ClassCaller, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+paths.EmbedPath, bytes.NewReader(body))
	if err != nil {
		return nil, domain.Usage{}, domain.ClassCaller, err
	}
	req.Header.Set("Content-Type", "application/json")
	authorize(req, paths.AuthStyle, attempt.Secret)
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
