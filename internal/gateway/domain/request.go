package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DefaultOffload is the token estimate at which a tool result is replaced
// with retrieve_output (UPM-25).
const DefaultOffload = 2000

const (
	retrieveOutput = "retrieve_output"
	findTool       = "find_tool"
)

// Tool is one canonical tool schema. Name order is the wire order.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// ToolCall is a structured tool invocation returned by a model or replayed in
// assistant history.
type ToolCall struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

// Supported ToolChoice modes.
const (
	ToolChoiceAuto = "auto"
	ToolChoiceAny  = "any"
	ToolChoiceNone = "none"
	ToolChoiceTool = "tool"
)

// ToolChoice controls how a model selects tools. Its zero value leaves the
// provider default in effect.
type ToolChoice struct {
	Mode string `json:"mode,omitempty"`
	Name string `json:"name,omitempty"`
}

// IsZero reports whether tc is unset.
func (tc ToolChoice) IsZero() bool {
	return strings.TrimSpace(tc.Mode) == "" && strings.TrimSpace(tc.Name) == ""
}

// UnmarshalJSON accepts a string ("auto", "any", "none", or a tool name) or an
// object ({"mode": ..., "name": ...}).
func (tc *ToolChoice) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*tc = ToolChoice{}
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		switch s {
		case "":
			*tc = ToolChoice{}
		case ToolChoiceAuto, ToolChoiceAny, ToolChoiceNone:
			*tc = ToolChoice{Mode: s}
		default:
			*tc = ToolChoice{Mode: ToolChoiceTool, Name: s}
		}
		return nil
	}
	type plain ToolChoice
	var p plain
	if err := json.Unmarshal(trimmed, &p); err != nil {
		return err
	}
	*tc = ToolChoice(p)
	return nil
}

// Role is a message speaker. System text lives on Request.System, never in
// Messages, so per-turn data cannot sit in the cache prefix.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn after the prefix. Tool results may be offloaded.
type Message struct {
	Role      Role       `json:"role"`
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
	ToolID    string     `json:"tool_id,omitempty"`
	IsError   bool       `json:"is_error,omitempty"`
	Offload   bool       `json:"offload,omitempty"`
}

// ChatResult is the normalised result of one non-streamed chat call.
type ChatResult struct {
	Text          string     `json:"text"`
	ToolCalls     []ToolCall `json:"tool_calls,omitempty"`
	StopReason    string     `json:"stop_reason,omitempty"`
	Usage         Usage      `json:"usage"`
	Model         string     `json:"model,omitempty"`
	CostUSD       *float64   `json:"cost_usd,omitempty"`
	CostMicroUSD  *int64     `json:"cost_micro_usd,omitempty"`
	ContextWindow int        `json:"context_window,omitempty"`
}

// Breakpoint is a cache mark on the prefix. Providers cache up to the last
// marked block of tools, then of system.
type Breakpoint string

const (
	BreakTools  Breakpoint = "tools"
	BreakSystem Breakpoint = "system"
)

// Request is the built prompt. Field order is the wire order: tools, system,
// messages. Nothing else is allowed before messages.
type Request struct {
	Tools    []Tool       `json:"tools,omitempty"`
	System   string       `json:"system,omitempty"`
	Messages []Message    `json:"messages"`
	Cache    []Breakpoint `json:"cache,omitempty"`
	// MaxOutputTokens and ToolChoice sit after the cache prefix so changing
	// them never invalidates cached tools or system blocks.
	MaxOutputTokens int        `json:"max_output_tokens,omitempty"`
	ToolChoice      ToolChoice `json:"tool_choice,omitzero"`
}

// Input is what a caller hands the builder.
type Input struct {
	Tools           []Tool
	AllowTools      []string // empty keeps every tool unless Mask is set
	Mask            bool     // when true, empty AllowTools means no tools
	CoreTools       bool     // when true, always include retrieve_output and find_tool
	Bundle          string   // named surface; resolved before Build
	System          string
	Messages        []Message
	OffloadAt       int // 0 means DefaultOffload
	ToolChoice      ToolChoice
	MaxOutputTokens int
}

// Build masks tools, canonicalises schemas, offloads large tool results, and
// stamps cache breakpoints after tools and after system when those sections
// are present. When tools are active, core tools (retrieve_output, find_tool)
// stay permanently in the static core tool prefix from turn 0 so offloading a
// tool result never mutates the tool prefix or breaks prompt caching.
func Build(in Input) (Request, error) {
	choice, err := normalizeToolChoice(in.ToolChoice)
	if err != nil {
		return Request{}, err
	}
	masked, err := maskTools(in.Tools, in.AllowTools, in.Mask)
	if err != nil {
		return Request{}, err
	}
	tools := make([]Tool, len(masked))
	copy(tools, masked)
	for i := range tools {
		schema, err := CanonicalSchema(tools[i].Schema)
		if err != nil {
			return Request{}, fmt.Errorf("gateway: tool %q schema: %w", tools[i].Name, err)
		}
		tools[i].Schema = schema
	}
	limit := in.OffloadAt
	if limit <= 0 {
		limit = DefaultOffload
	}
	messages := make([]Message, 0, len(in.Messages))
	offloaded := false
	for _, msg := range in.Messages {
		if msg.Role == "" {
			return Request{}, fmt.Errorf("gateway: message role is empty")
		}
		if msg.Role != RoleUser && msg.Role != RoleAssistant && msg.Role != RoleTool {
			return Request{}, fmt.Errorf("gateway: message role %q", msg.Role)
		}
		if msg.Role == RoleTool && TokenEstimate(msg.Text) > limit {
			msg.Text = retrieveOutput
			msg.Offload = true
			offloaded = true
		}
		messages = append(messages, msg)
	}
	var finalTools []Tool
	if len(tools) > 0 || in.CoreTools || offloaded {
		core, err := coreTools()
		if err != nil {
			return Request{}, err
		}
		finalTools = make([]Tool, 0, len(core)+len(tools))
		finalTools = append(finalTools, core...)
		for _, t := range tools {
			if t.Name == retrieveOutput || t.Name == findTool {
				continue
			}
			finalTools = append(finalTools, t)
		}
	}
	out := Request{
		Tools:           finalTools,
		System:          in.System,
		Messages:        messages,
		MaxOutputTokens: in.MaxOutputTokens,
		ToolChoice:      choice,
	}
	if len(finalTools) > 0 {
		out.Cache = append(out.Cache, BreakTools)
	}
	if strings.TrimSpace(in.System) != "" {
		out.Cache = append(out.Cache, BreakSystem)
	}
	return out, nil
}

func coreTools() ([]Tool, error) {
	retrieveSchema, err := CanonicalSchema(json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`))
	if err != nil {
		return nil, err
	}
	findSchema, err := CanonicalSchema(json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`))
	if err != nil {
		return nil, err
	}
	return []Tool{
		{
			Name:        retrieveOutput,
			Description: "fetch an offloaded tool result",
			Schema:      retrieveSchema,
		},
		{
			Name:        findTool,
			Description: "search available tools by name or description",
			Schema:      findSchema,
		},
	}, nil
}

func normalizeToolChoice(tc ToolChoice) (ToolChoice, error) {
	mode := strings.ToLower(strings.TrimSpace(tc.Mode))
	name := strings.TrimSpace(tc.Name)
	if mode == "" && name == "" {
		return ToolChoice{}, nil
	}
	if mode == "" {
		mode = ToolChoiceTool
	}
	switch mode {
	case ToolChoiceAuto, ToolChoiceAny, ToolChoiceNone:
		if name != "" {
			return ToolChoice{}, fmt.Errorf("gateway: tool choice %q takes no name", mode)
		}
		return ToolChoice{Mode: mode}, nil
	case ToolChoiceTool:
		if name == "" {
			return ToolChoice{}, fmt.Errorf("gateway: tool choice %q needs a tool name", mode)
		}
		return ToolChoice{Mode: mode, Name: name}, nil
	default:
		return ToolChoice{}, fmt.Errorf("gateway: tool choice mode %q", mode)
	}
}

// CanonicalSchema rewrites a JSON object with sorted keys so a tool schema is
// stable across calls.
func CanonicalSchema(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return marshalSorted(v)
}

func marshalSorted(v any) (json.RawMessage, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b bytes.Buffer
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			vb, err := marshalSorted(t[k])
			if err != nil {
				return nil, err
			}
			b.Write(kb)
			b.WriteByte(':')
			b.Write(vb)
		}
		b.WriteByte('}')
		return b.Bytes(), nil
	case []any:
		var b bytes.Buffer
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			vb, err := marshalSorted(item)
			if err != nil {
				return nil, err
			}
			b.Write(vb)
		}
		b.WriteByte(']')
		return b.Bytes(), nil
	default:
		return json.Marshal(t)
	}
}

func maskTools(tools []Tool, allow []string, mask bool) ([]Tool, error) {
	if !mask && len(allow) == 0 {
		return tools, nil
	}
	keep := map[string]bool{}
	for _, name := range allow {
		if name == "" {
			return nil, fmt.Errorf("gateway: allowed tool name is empty")
		}
		keep[name] = true
	}
	var out []Tool
	for _, tool := range tools {
		if tool.Name == "" {
			return nil, fmt.Errorf("gateway: tool name is empty")
		}
		if keep[tool.Name] {
			out = append(out, tool)
		}
	}
	return out, nil
}

// TokenEstimate is a cheap 4-byte heuristic used only to decide offload.
func TokenEstimate(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// OffloadSentinel is the tool result body after offload. Adapters may send
// this name as a retrieve_output tool the model can call.
func OffloadSentinel() string { return retrieveOutput }
