// Package anthropic is the adapter for the Anthropic Messages API. It does
// chat, streamed chat, decide and the probe. It has no Embed method, so the
// gateway lists no embed adapter for it (see gateway.Mount).
package anthropic

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
)

// Version is the API version sent in the anthropic-version header.
const Version = "2023-06-01"

// DefaultMaxTokens caps a reply when the request sets no cap. The API will
// not take a call without one.
const DefaultMaxTokens = 4096

// statusOverloaded is the status Anthropic answers with when it is short of
// capacity.
const statusOverloaded = 529

// Adapter talks to the Messages API. Production dials through netguard;
// tests inject Client.
type Adapter struct {
	Client *http.Client
	// Lookup resolves host names for the guarded clients. Nil uses the system
	// resolver. Tests set it to re-point a name.
	Lookup wire.Lookup
}

func (a Adapter) client(attempt app.Attempt) *http.Client {
	return wire.Client(a.Client, a.Lookup, attempt.PlanKind)
}

// target returns the account's base URL and the paths of the Anthropic
// vendor. ok is false when the account has no base URL.
func target(attempt app.Attempt) (base string, paths domain.Vendor, ok bool) {
	base = strings.TrimRight(attempt.BaseURL, "/")
	if base == "" {
		return "", domain.Vendor{}, false
	}
	paths, _ = domain.VendorFor(domain.AdapterAnthropic)
	return base, paths, true
}

// statusClass is the shared mapping, plus Anthropic's own overloaded status.
func statusClass(code int) (domain.FailureClass, error) {
	if code == statusOverloaded {
		return domain.ClassOverloaded, fmt.Errorf("gateway: provider %d", code)
	}
	return wire.StatusClass(code)
}

// request builds a call with the headers every Messages call carries.
func request(ctx context.Context, attempt app.Attempt, method, endpoint string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("anthropic-version", Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if attempt.Secret != "" {
		req.Header.Set("x-api-key", attempt.Secret)
	}
	return req, nil
}

// Probe asks for the model list, the cheapest call that needs the key. It
// returns the failure class of the answer.
func (a Adapter) Probe(ctx context.Context, attempt app.Attempt) (domain.FailureClass, error) {
	base, paths, ok := target(attempt)
	if !ok {
		return domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	req, err := request(ctx, attempt, http.MethodGet, base+paths.ModelsPath, nil)
	if err != nil {
		return domain.ClassCaller, err
	}
	res, err := a.client(attempt).Do(req)
	if err != nil {
		return wire.ClassForErr(err), err
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
	body, err := json.Marshal(buildPayload(attempt.Model, req, true))
	if err != nil {
		return domain.ChatResult{}, domain.ClassCaller, err
	}
	httpReq, err := request(ctx, attempt, http.MethodPost, base+paths.ChatPath, body)
	if err != nil {
		return domain.ChatResult{}, domain.ClassCaller, err
	}
	res, err := a.client(attempt).Do(httpReq)
	if err != nil {
		return domain.ChatResult{}, wire.ClassForErr(err), err
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

func ephemeral() *cacheCtl { return &cacheCtl{Type: "ephemeral"} }

// textBlock is one block of the system prompt.
type textBlock struct {
	Type         string    `json:"type"`
	Text         string    `json:"text"`
	CacheControl *cacheCtl `json:"cache_control,omitempty"`
}

type toolDef struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheCtl       `json:"cache_control,omitempty"`
}

type toolChoiceBody struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// payload is the Messages request. Field order is tools, system, messages so
// the cache prefix sits in front of the turn; the cap, tool_choice and the
// stream flag follow it.
type payload struct {
	Model      string          `json:"model"`
	Tools      []toolDef       `json:"tools,omitempty"`
	System     []textBlock     `json:"system,omitempty"`
	Messages   []message       `json:"messages"`
	MaxTokens  int             `json:"max_tokens"`
	ToolChoice *toolChoiceBody `json:"tool_choice,omitempty"`
	Stream     bool            `json:"stream,omitempty"`
}

// objectSchema is the input schema of a tool that names none. The API
// requires one, and it must be an object schema.
const objectSchema = `{"type":"object"}`

// buildPayload maps a built request onto the Messages API. A cache breakpoint
// on tools marks the last tool, and one on system marks the system block:
// each caches everything up to and including the block it marks. Consecutive
// RoleTool messages are merged into a single user message of tool_result
// blocks as required by the Messages API.
func buildPayload(model string, req domain.Request, stream bool) payload {
	breakTools, breakSystem := false, false
	for _, mark := range req.Cache {
		if mark == domain.BreakTools {
			breakTools = true
		}
		if mark == domain.BreakSystem {
			breakSystem = true
		}
	}
	out := payload{
		Model:      model,
		MaxTokens:  req.MaxOutputTokens,
		ToolChoice: encodeToolChoice(req.ToolChoice),
		Stream:     stream,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = DefaultMaxTokens
	}
	for i, tool := range req.Tools {
		item := toolDef{Name: tool.Name, Description: tool.Description, InputSchema: tool.Schema}
		// domain.Build writes an absent schema as {}.
		if schema := bytes.TrimSpace(item.InputSchema); len(schema) == 0 || string(schema) == "{}" {
			item.InputSchema = json.RawMessage(objectSchema)
		}
		if breakTools && i == len(req.Tools)-1 {
			item.CacheControl = ephemeral()
		}
		out.Tools = append(out.Tools, item)
	}
	if strings.TrimSpace(req.System) != "" {
		block := textBlock{Type: "text", Text: req.System}
		if breakSystem {
			block.CacheControl = ephemeral()
		}
		out.System = []textBlock{block}
	}
	out.Messages = make([]message, 0, len(req.Messages))
	lastWasToolResult := false
	for _, msg := range req.Messages {
		switch msg.Role {
		case domain.RoleAssistant:
			lastWasToolResult = false
			if len(msg.ToolCalls) == 0 {
				out.Messages = append(out.Messages, message{Role: "assistant", Content: msg.Text})
				continue
			}
			blocks := make([]contentBlock, 0, len(msg.ToolCalls)+1)
			if msg.Text != "" {
				blocks = append(blocks, contentBlock{Type: "text", Text: msg.Text})
			}
			for _, tc := range msg.ToolCalls {
				input := tc.Arguments
				if len(bytes.TrimSpace(input)) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, contentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: input,
				})
			}
			out.Messages = append(out.Messages, message{Role: "assistant", Content: blocks})
		case domain.RoleTool:
			block := toolResultBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolID,
				Content:   msg.Text,
				IsError:   msg.IsError,
			}
			if lastWasToolResult && len(out.Messages) > 0 {
				prev, _ := out.Messages[len(out.Messages)-1].Content.([]toolResultBlock)
				out.Messages[len(out.Messages)-1].Content = append(prev, block)
				continue
			}
			out.Messages = append(out.Messages, message{Role: "user", Content: []toolResultBlock{block}})
			lastWasToolResult = true
		default:
			lastWasToolResult = false
			out.Messages = append(out.Messages, message{Role: "user", Content: msg.Text})
		}
	}
	return out
}

func encodeToolChoice(tc domain.ToolChoice) *toolChoiceBody {
	if tc.IsZero() {
		return nil
	}
	switch tc.Mode {
	case domain.ToolChoiceAuto:
		return &toolChoiceBody{Type: "auto"}
	case domain.ToolChoiceAny:
		return &toolChoiceBody{Type: "any"}
	case domain.ToolChoiceNone:
		return &toolChoiceBody{Type: "none"}
	case domain.ToolChoiceTool:
		return &toolChoiceBody{Type: "tool", Name: tc.Name}
	default:
		return nil
	}
}

// usageBlock is the usage object of a Messages answer, in full on the
// non-streamed reply and in parts on the streamed events. A field the
// provider leaves out or sets to null stays as it was.
type usageBlock struct {
	InputTokens   *int `json:"input_tokens"`
	OutputTokens  *int `json:"output_tokens"`
	CacheCreation *int `json:"cache_creation_input_tokens"`
	CacheRead     *int `json:"cache_read_input_tokens"`
}

// into records the counts that arrived. Anthropic counts input tokens apart
// from the cache reads and writes, as domain.Usage does.
func (u usageBlock) into(usage *domain.Usage) {
	usage.Arrived = true
	if u.InputTokens != nil {
		usage.Input = *u.InputTokens
	}
	if u.OutputTokens != nil {
		usage.Output = *u.OutputTokens
	}
	if u.CacheRead != nil {
		usage.CacheRead = *u.CacheRead
	}
	if u.CacheCreation != nil {
		usage.CacheWrite = *u.CacheCreation
	}
}

func (a Adapter) complete(ctx context.Context, attempt app.Attempt, built domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	base, paths, ok := target(attempt)
	if !ok {
		return domain.ChatResult{}, domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	body, err := json.Marshal(buildPayload(attempt.Model, built, false))
	if err != nil {
		return domain.ChatResult{}, domain.ClassCaller, err
	}
	req, err := request(ctx, attempt, http.MethodPost, base+paths.ChatPath, body)
	if err != nil {
		return domain.ChatResult{}, domain.ClassCaller, err
	}
	res, err := a.client(attempt).Do(req)
	if err != nil {
		return domain.ChatResult{}, wire.ClassForErr(err), err
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
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string      `json:"stop_reason"`
		Usage      *usageBlock `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || (len(parsed.Content) == 0 && parsed.Usage == nil) {
		return domain.ChatResult{}, domain.ClassCaller, fmt.Errorf("gateway: provider body")
	}
	var text strings.Builder
	var toolCalls []domain.ToolCall
	for _, block := range parsed.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			args := block.Input
			if len(bytes.TrimSpace(args)) == 0 {
				args = json.RawMessage("{}")
			}
			toolCalls = append(toolCalls, domain.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		}
	}
	usage := domain.Usage{}
	if parsed.Usage != nil {
		parsed.Usage.into(&usage)
	}
	return domain.ChatResult{
		Text:       text.String(),
		ToolCalls:  toolCalls,
		StopReason: parsed.StopReason,
		Usage:      usage,
		Model:      attempt.Model,
	}, domain.ClassOK, nil
}

// streamEvent is one SSE data payload of a streamed Messages answer. Its
// type says what it is, whatever the event: line before it says.
type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *usageBlock `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usageBlock `json:"usage"`
	Error *struct {
		Type string `json:"type"`
	} `json:"error"`
}

type streamToolAcc struct {
	id           string
	name         string
	initialInput json.RawMessage
	args         strings.Builder
}

type streamState struct {
	model       string
	usage       domain.Usage
	text        strings.Builder
	stopReason  string
	blockToTool map[int]int
	tools       []streamToolAcc
}

func (s *streamState) result() domain.ChatResult {
	var calls []domain.ToolCall
	if len(s.tools) > 0 {
		calls = make([]domain.ToolCall, len(s.tools))
		for i, acc := range s.tools {
			raw := strings.TrimSpace(acc.args.String())
			var args json.RawMessage
			switch {
			case raw != "":
				args = json.RawMessage(raw)
			case len(bytes.TrimSpace(acc.initialInput)) > 0:
				args = acc.initialInput
			default:
				args = json.RawMessage("{}")
			}
			calls[i] = domain.ToolCall{
				ID:        acc.id,
				Name:      acc.name,
				Arguments: args,
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

// readStreamTools decodes the SSE events of a streamed Messages answer. The data:
// lines of one event are one JSON object, dispatched at the blank line that
// ends the event (a pending event at the end of the body dispatches too);
// comment lines and other fields are ignored. Text and tool-call deltas go
// through events in order, message_start and message_delta bring the usage,
// and message_stop ends the stream. A ping and an event of a kind not listed
// here are skipped. An error event ends the stream with the class of its error
// type. A stream that ends any other way was cut short.
func readStreamTools(ctx context.Context, model string, body io.Reader, events app.StreamEvents) (domain.ChatResult, domain.FailureClass, error) {
	in := bufio.NewReader(body)
	state := &streamState{
		model:       model,
		blockToTool: map[int]int{},
	}
	var data []string
	// event runs the collected data lines as one event. done reports whether
	// the stream ended there.
	event := func() (class domain.FailureClass, err error, done bool) {
		joined := strings.Join(data, "\n")
		data = nil
		return oneEvent(joined, state, events)
	}
	for {
		line, rerr := in.ReadString('\n')
		if text := strings.TrimSpace(line); text != "" {
			if joined, ok := strings.CutPrefix(text, "data:"); ok {
				data = append(data, strings.TrimSpace(joined))
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
					return state.result(), domain.ClassCancelled, ctx.Err()
				}
				return state.result(), domain.ClassTransient, errors.New("gateway: stream ended without message_stop")
			}
			if ctx.Err() != nil {
				return state.result(), domain.ClassCancelled, rerr
			}
			return state.result(), wire.ClassForErr(rerr), rerr
		}
	}
}

func oneEvent(joined string, state *streamState, events app.StreamEvents) (class domain.FailureClass, err error, done bool) {
	var ev streamEvent
	if err := json.Unmarshal([]byte(joined), &ev); err != nil {
		return domain.ClassCaller, fmt.Errorf("gateway: provider body"), true
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil && ev.Message.Usage != nil {
			ev.Message.Usage.into(&state.usage)
		}
	case "content_block_start":
		if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
			toolIdx := len(state.tools)
			state.blockToTool[ev.Index] = toolIdx
			state.tools = append(state.tools, streamToolAcc{
				id:           ev.ContentBlock.ID,
				name:         ev.ContentBlock.Name,
				initialInput: ev.ContentBlock.Input,
			})
			if events.ToolCall != nil {
				if err := events.ToolCall(app.ToolCallDelta{
					Index: toolIdx,
					ID:    ev.ContentBlock.ID,
					Name:  ev.ContentBlock.Name,
				}); err != nil {
					return domain.ClassCancelled, err, true
				}
			}
		}
	case "content_block_delta":
		if ev.Delta == nil {
			break
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				state.text.WriteString(ev.Delta.Text)
				if events.Text != nil {
					if err := events.Text(ev.Delta.Text); err != nil {
						return domain.ClassCancelled, err, true
					}
				}
			}
		case "input_json_delta":
			toolIdx, ok := state.blockToTool[ev.Index]
			if !ok {
				break
			}
			if ev.Delta.PartialJSON != "" {
				state.tools[toolIdx].args.WriteString(ev.Delta.PartialJSON)
				if events.ToolCall != nil {
					if err := events.ToolCall(app.ToolCallDelta{
						Index:          toolIdx,
						ArgumentsDelta: ev.Delta.PartialJSON,
					}); err != nil {
						return domain.ClassCancelled, err, true
					}
				}
			}
		}
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			state.stopReason = ev.Delta.StopReason
		}
		if ev.Usage != nil {
			ev.Usage.into(&state.usage)
		}
	case "message_stop":
		return domain.ClassOK, nil, true
	case "error":
		kind := ""
		if ev.Error != nil {
			kind = ev.Error.Type
		}
		failed := errorClass(kind)
		return failed, fmt.Errorf("gateway: provider stream error: %s", failed), true
	}
	return domain.ClassOK, nil, false
}

// errorClass maps the type of an in-stream error to a failure class. The
// type is the provider's text, so it is only compared, never shown.
func errorClass(kind string) domain.FailureClass {
	switch kind {
	case "overloaded_error":
		return domain.ClassOverloaded
	case "rate_limit_error":
		return domain.ClassRateLimit
	case "authentication_error", "permission_error":
		return domain.ClassCredential
	case "not_found_error":
		return domain.ClassModelAccess
	case "invalid_request_error", "request_too_large":
		return domain.ClassCaller
	default:
		return domain.ClassTransient
	}
}
