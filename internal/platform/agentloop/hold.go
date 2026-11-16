package agentloop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxActionRunes caps deterministic hold action strings (matching tasks.approvals constraint).
	MaxActionRunes = 200
	// MaxPreviewRunes caps deterministic argument previews.
	MaxPreviewRunes = 1000
)

// novelSinkKeys lists parameter names that carry outbound target/recipient
// sinks and trigger untrusted_origin when the run is tainted.
var novelSinkKeys = map[string]bool{
	"iban":         true,
	"recipient":    true,
	"to":           true,
	"target_url":   true,
	"webhook_url":  true,
	"callback_url": true,
}

// ignoredModelSummaryKeys lists argument keys that carry model-written
// summaries which must never be used as deterministic approval action/preview text.
var ignoredModelSummaryKeys = map[string]bool{
	"summary":     true,
	"description": true,
	"preview":     true,
	"action_text": true,
}

// HeldCall is one mutating tool call collected into a turn's batch hold.
type HeldCall struct {
	CallID          string          `json:"call_id"`
	ToolName        string          `json:"tool_name"`
	Op              ToolOp          `json:"op"`
	Target          string          `json:"target,omitempty"`
	Arguments       json.RawMessage `json:"arguments,omitempty"`
	ArgsDigest      string          `json:"args_digest,omitempty"`
	Action          string          `json:"action"`
	Preview         string          `json:"preview"`
	UntrustedOrigin bool            `json:"untrusted_origin"`
	NovelSinks      []string        `json:"novel_sinks,omitempty"`
}

// BatchHold groups all held change/delete calls from a single turn.
type BatchHold struct {
	Reason          string     `json:"reason"`
	Action          string     `json:"action"`
	Preview         string     `json:"preview"`
	Calls           []HeldCall `json:"calls"`
	UntrustedOrigin bool       `json:"untrusted_origin"`
}

// CallsJSONB returns each held call encoded as an individual JSON object for
// Postgres jsonb[] columns.
func (h BatchHold) CallsJSONB() ([][]byte, error) {
	out := make([][]byte, 0, len(h.Calls))
	for _, c := range h.Calls {
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

// DetectNovelSinks inspects raw JSON arguments recursively and returns sorted
// novel sink parameter names with non-empty values.
func DetectNovelSinks(raw json.RawMessage) []string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	var parsed any
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return nil
	}
	found := make(map[string]bool)
	scanNovelSinks(parsed, found)
	if len(found) == 0 {
		return nil
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func scanNovelSinks(v any, found map[string]bool) {
	switch node := v.(type) {
	case map[string]any:
		for k, val := range node {
			norm := strings.ToLower(strings.TrimSpace(k))
			if novelSinkKeys[norm] && hasNonEmptyValue(val) {
				found[norm] = true
			}
			scanNovelSinks(val, found)
		}
	case []any:
		for _, item := range node {
			scanNovelSinks(item, found)
		}
	}
}

func hasNonEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

// RenderCallPreview deterministically renders action and preview strings in Go
// from parsed arguments in sorted key order, never trusting model-written
// summary fields.
func RenderCallPreview(toolName string, op ToolOp, args json.RawMessage) (action, preview string) {
	if op == "" {
		op = OpChange
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "tool"
	}
	parsed := parseArgMap(args)
	parts := formatSortedPairs(parsed)
	joined := strings.Join(parts, ", ")
	if joined == "" {
		action = fmt.Sprintf("%s %s", op, toolName)
		preview = fmt.Sprintf("%s %s()", op, toolName)
	} else {
		action = fmt.Sprintf("%s %s (%s)", op, toolName, joined)
		preview = fmt.Sprintf("%s %s(%s)", op, toolName, joined)
	}
	return truncateRunes(action, MaxActionRunes), truncateRunes(preview, MaxPreviewRunes)
}

// CallDigest computes a deterministic SHA-256 hex digest binding tool name,
// operation, and canonicalized arguments (or normalized raw payload if non-object).
func CallDigest(name string, op ToolOp, raw json.RawMessage) string {
	if op == "" {
		op = OpChange
	}
	var args any
	if parsed := parseArgMap(raw); parsed != nil {
		args = parsed
	} else if trimmed := strings.TrimSpace(string(raw)); trimmed != "" {
		args = "raw:" + trimmed
	} else {
		args = map[string]any{}
	}
	payload := struct {
		Name string `json:"name"`
		Op   ToolOp `json:"op"`
		Args any    `json:"args"`
	}{
		Name: strings.TrimSpace(name),
		Op:   op,
		Args: args,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// BuildHeldCall constructs a HeldCall with deterministic rendering and novel-sink
// untrusted_origin tagging when tainted is true.
func BuildHeldCall(call ToolCall, def ToolDef, target string, tainted bool) HeldCall {
	op := def.Op
	if op == "" {
		op = OpChange
	}
	action, preview := RenderCallPreview(call.Name, op, call.Arguments)
	sinks := DetectNovelSinks(call.Arguments)
	return HeldCall{
		CallID:          call.ID,
		ToolName:        call.Name,
		Op:              op,
		Target:          target,
		Arguments:       bytes.Clone(call.Arguments),
		ArgsDigest:      CallDigest(call.Name, op, call.Arguments),
		Action:          action,
		Preview:         preview,
		UntrustedOrigin: tainted && len(sinks) > 0,
		NovelSinks:      sinks,
	}
}

// BuildBatchHold combines one or more HeldCalls from a turn into one BatchHold.
func BuildBatchHold(reason string, calls []HeldCall) BatchHold {
	if reason == "" {
		reason = "ask"
	}
	var untrusted bool
	previews := make([]string, 0, len(calls))
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		if c.UntrustedOrigin {
			untrusted = true
		}
		previews = append(previews, c.Preview)
		names = append(names, c.ToolName)
	}
	var action string
	if len(calls) == 1 {
		action = calls[0].Action
	} else if len(calls) > 1 {
		action = truncateRunes(fmt.Sprintf("%d actions pending approval: %s", len(calls), strings.Join(names, ", ")), MaxActionRunes)
	}
	return BatchHold{
		Reason:          reason,
		Action:          action,
		Preview:         truncateRunes(strings.Join(previews, "\n"), MaxPreviewRunes),
		Calls:           calls,
		UntrustedOrigin: untrusted,
	}
}

// DryRunResult constructs the deterministic preview and synthetic tool result
// JSON for a dry_run=true shadow-simulated write.
func DryRunResult(toolName string, op ToolOp, args json.RawMessage, untrustedOrigin bool) (action, preview, syntheticOutput string) {
	action, preview = RenderCallPreview(toolName, op, args)
	payload, _ := json.Marshal(struct {
		Status          string `json:"status"`
		Tool            string `json:"tool"`
		Action          string `json:"action"`
		Preview         string `json:"preview"`
		UntrustedOrigin bool   `json:"untrusted_origin,omitempty"`
	}{
		Status:          string(ActionStateSimulated),
		Tool:            toolName,
		Action:          action,
		Preview:         preview,
		UntrustedOrigin: untrustedOrigin,
	})
	return action, preview, string(payload)
}

func parseArgMap(raw json.RawMessage) map[string]any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil
	}
	return m
}

func formatSortedPairs(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if ignoredModelSummaryKeys[strings.ToLower(strings.TrimSpace(k))] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%s", k, formatValue(m[k])))
	}
	return out
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return fmt.Sprintf("%q", truncateRunes(redactCredentialLike(x), 80))
	case float64, bool:
		return fmt.Sprintf("%v", x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "<value>"
		}
		return truncateRunes(redactCredentialLike(string(b)), 80)
	}
}

func redactCredentialLike(s string) string {
	low := strings.ToLower(s)
	for _, mark := range []string{"upmo_pat_", "upmo_s2s_", "upmo_svc_", "bearer ", "sk-"} {
		if strings.Contains(low, mark) {
			return "[redacted]"
		}
	}
	return s
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
