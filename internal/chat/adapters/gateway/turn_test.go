package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

func i64p(v int64) *int64 { return &v }

func TestTurnsCompleteMapsPromptAndResponse(t *testing.T) {
	secret := serviceKey(t, 'k')
	calls := 0
	var body map[string]any
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		done := `{"done":{"model":"m2","input_tokens":3,"output_tokens":4,"cache_read":1,"cache_write":2,"reasoning":1,"cost_micro_usd":17,"tool_calls":[{"id":"t1","name":"lookup","arguments":{"q":1}}],"stop_reason":"tool_calls"}}`
		if calls == 2 {
			done = `{"done":{"model":"m2","input_tokens":3,"output_tokens":4,"cache_read":1,"cache_write":2,"reasoning":1,"cost_micro_usd":null,"tool_calls":[{"id":"t1","name":"lookup","arguments":{"q":1}}],"stop_reason":"tool_calls"}}`
		}
		_, _ = fmt.Fprintf(w, "%s\n%s\n%s\n", `{"delta":"Hel"}`, `{"delta":"lo"}`, done)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	var deltas []string
	model := Turns{Client: client}.Bind(app.TurnRoute{Route: "draft", ChatID: "chat-1"}, func(delta string) {
		deltas = append(deltas, delta)
	}, nil)
	prompt := agentloop.PromptRequest{
		UserID:  "u",
		AgentID: "018f3b2c-7a10-7c4a-8e11-0000000000a1",
		RunID:   "018f3b2c-7a10-7c4a-8e11-0000000000b2",
		Tools: []agentloop.ToolDef{{
			Name:        "lookup",
			Description: "find",
			Schema:      json.RawMessage(`{"type":"object"}`),
			Category:    agentloop.CategoryDirect,
			Op:          agentloop.OpRead,
		}},
		Messages: []agentloop.Message{
			{Role: agentloop.RoleUser, Text: "hi"},
			{Role: agentloop.RoleTool, ToolName: "pay", ToolID: "c9", Text: "awaiting approval", HeldPlaceholder: true},
		},
	}
	resp, err := model.Complete(t.Context(), prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := agentloop.PromptResponse{
		Text:         "Hello",
		ToolCalls:    []agentloop.ToolCall{{ID: "t1", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`)}},
		StopReason:   "tool_calls",
		Model:        "m2",
		Usage:        kernel.TokenUsage{InputTokens: 3, OutputTokens: 4, CacheReadTokens: 1, CacheWriteTokens: 2, ReasoningTokens: 1},
		CostMicroUSD: i64p(17),
	}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("response = %+v, want %+v", resp, want)
	}
	if !reflect.DeepEqual(deltas, []string{"Hel", "lo"}) {
		t.Fatalf("deltas = %v", deltas)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	tool, _ := tools[0].(map[string]any)
	if len(tool) != 3 || tool["name"] != "lookup" || tool["description"] != "find" || tool["schema"] == nil {
		t.Fatalf("tools[0] = %#v, want only name, description and schema", tool)
	}
	if body["agent_id"] != "018f3b2c-7a10-7c4a-8e11-0000000000a1" || body["run_id"] != "018f3b2c-7a10-7c4a-8e11-0000000000b2" {
		t.Fatalf("agent_id = %v, run_id = %v", body["agent_id"], body["run_id"])
	}
	if body["core_tools"] != true {
		t.Fatalf("core_tools = %v", body["core_tools"])
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	placeholder, _ := messages[1].(map[string]any)
	if _, has := placeholder["held_placeholder"]; has || placeholder["text"] != "awaiting approval" {
		t.Fatalf("held placeholder message = %#v", placeholder)
	}

	resp, err = model.Complete(t.Context(), prompt)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CostMicroUSD != nil {
		t.Fatalf("null cost = %v, want nil", resp.CostMicroUSD)
	}
}

func TestTurnsCompleteMapsGatewayFailuresToModelError(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n", `{"error":{"failure_class":"rate_limit","model":"m1","message":"the provider rate limited the request"}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	model := Turns{Client: client}.Bind(app.TurnRoute{Route: "draft", ChatID: "chat-1"}, nil, nil)
	_, err = model.Complete(t.Context(), agentloop.PromptRequest{
		UserID:   "u",
		Messages: []agentloop.Message{{Role: agentloop.RoleUser, Text: "hi"}},
	})
	var modelErr *app.ModelError
	if !errors.As(err, &modelErr) {
		t.Fatalf("err = %v", err)
	}
	if modelErr.Class != "rate_limit" || modelErr.Model != "m1" {
		t.Fatalf("model error = %+v", modelErr)
	}
}
