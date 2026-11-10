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

const retrieveOutput = "retrieve_output"

// Tool is one canonical tool schema. Name order is the wire order.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
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
	Role     Role   `json:"role"`
	Text     string `json:"text,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
	ToolID   string `json:"tool_id,omitempty"`
	Offload  bool   `json:"offload,omitempty"`
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
	// MaxOutputTokens caps the reply; 0 means the adapter default. It is not
	// part of the cache prefix, so it sits after the wire fields.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
}

// Input is what a caller hands the builder.
type Input struct {
	Tools      []Tool
	AllowTools []string // empty keeps every tool unless Mask is set
	Mask       bool     // when true, empty AllowTools means no tools
	Bundle     string   // named surface; resolved before Build
	System     string
	Messages   []Message
	OffloadAt  int // 0 means DefaultOffload
}

// Build masks tools, canonicalises schemas, offloads large tool results, and
// stamps cache breakpoints after tools and after system when those sections
// are present.
func Build(in Input) (Request, error) {
	tools, err := maskTools(in.Tools, in.AllowTools, in.Mask)
	if err != nil {
		return Request{}, err
	}
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
	if offloaded {
		schema, err := CanonicalSchema(json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`))
		if err != nil {
			return Request{}, err
		}
		tools = append(tools, Tool{
			Name:        retrieveOutput,
			Description: "fetch an offloaded tool result",
			Schema:      schema,
		})
	}
	out := Request{Tools: tools, System: in.System, Messages: messages}
	if len(tools) > 0 {
		out.Cache = append(out.Cache, BreakTools)
	}
	if strings.TrimSpace(in.System) != "" {
		out.Cache = append(out.Cache, BreakSystem)
	}
	return out, nil
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
