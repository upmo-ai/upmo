//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	chatgateway "github.com/upmo-ai/upmo/internal/chat/adapters/gateway"
	chatapp "github.com/upmo-ai/upmo/internal/chat/app"
	httpapi "github.com/upmo-ai/upmo/internal/gateway/adapters/http"
	openaiadapter "github.com/upmo-ai/upmo/internal/gateway/adapters/openai"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

// toolStreamAdapter records the request it got and streams text, then
// tool-call deltas, like the fake adapter but on the openai adapter key.
type toolStreamAdapter struct {
	calls int
	got   *domain.Request
}

func (a *toolStreamAdapter) ChatStream(ctx context.Context, attempt app.Attempt, req domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	res, class, err := a.ChatStreamTools(ctx, attempt, req, app.StreamEvents{Text: emit})
	return res.Usage, class, err
}

func (a *toolStreamAdapter) ChatStreamTools(_ context.Context, _ app.Attempt, req domain.Request, events app.StreamEvents) (domain.ChatResult, domain.FailureClass, error) {
	a.calls++
	saved := req
	a.got = &saved
	for _, text := range []string{"a ", "b "} {
		if err := events.Text(text); err != nil {
			return domain.ChatResult{}, domain.ClassCanceled, err
		}
	}
	calls := []domain.ToolCall{
		{ID: "t1", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`)},
		{ID: "t2", Name: "search", Arguments: json.RawMessage(`{"q":2}`)},
	}
	for i, call := range calls {
		if err := events.ToolCall(app.ToolCallDelta{Index: i, ID: call.ID, Name: call.Name}); err != nil {
			return domain.ChatResult{}, domain.ClassCanceled, err
		}
		if err := events.ToolCall(app.ToolCallDelta{Index: i, ArgumentsDelta: string(call.Arguments)}); err != nil {
			return domain.ChatResult{}, domain.ClassCanceled, err
		}
	}
	return domain.ChatResult{
		ToolCalls:  calls,
		StopReason: "tool_calls",
		Usage:      domain.Usage{Arrived: true, Input: 100, Output: 50, CacheRead: 25, CacheWrite: 10, Reasoning: 5},
		Model:      "m1",
	}, domain.ClassOK, nil
}

// chatTurnsWorld is a pin world whose stream adapter records what crosses the
// real gateway, with the chat Turns client wired to its HTTP handler.
func chatTurnsWorld(t *testing.T) (pinWorld, *toolStreamAdapter, chatgateway.Client) {
	t.Helper()
	w := newPinWorld(t, nil)
	seedFakeVendorRate(t, &w, 75.0)
	adapter := &toolStreamAdapter{}
	w.disp.Stream[domain.AdapterOpenAI] = adapter
	mux := http.NewServeMux()
	httpapi.Handler{Dispatch: w.disp}.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	secret := "upmo_svc_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	client, err := chatgateway.Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	return w, adapter, client
}

func TestFeature_UPM310a_ChatTurnsCrossTheRealGateway(t *testing.T) {
	w, adapter, client := chatTurnsWorld(t)
	const (
		agentID = "018f3b2c-7a10-7c4a-8e11-0000000000a1"
		runID   = "018f3b2c-7a10-7c4a-8e11-0000000000b2"
		chatID  = "018f3b2c-7a10-7c4a-8e11-0000000000c3"
	)
	var deltas []string
	model := chatgateway.Turns{Client: client}.Bind(chatapp.TurnRoute{Route: "pin-house", ChatID: chatID}, func(delta string) {
		deltas = append(deltas, delta)
	}, nil)
	resp, err := model.Complete(context.Background(), agentloop.PromptRequest{
		UserID:  pinAuthor,
		AgentID: agentID,
		RunID:   runID,
		Tools:   []agentloop.ToolDef{{Name: "lookup", Description: "find", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []agentloop.Message{
			{Role: agentloop.RoleUser, Text: "q"},
			{Role: agentloop.RoleAssistant, ToolCalls: []agentloop.ToolCall{{ID: "c0", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`), ThoughtSignature: "sig"}}},
			{Role: agentloop.RoleTool, ToolName: "lookup", ToolID: "c0", Text: "boom", IsError: true, Offload: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.calls != 1 || adapter.got == nil {
		t.Fatalf("adapter calls = %d", adapter.calls)
	}
	names := make([]string, 0, len(adapter.got.Tools))
	for _, tool := range adapter.got.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"retrieve_output", "find_tool", "lookup"}) {
		t.Fatalf("tool names = %v", names)
	}
	wantCall := domain.ToolCall{ID: "c0", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`), ThoughtSignature: "sig"}
	if !reflect.DeepEqual(adapter.got.Messages[1].ToolCalls[0], wantCall) {
		t.Fatalf("replayed call = %+v, want %+v", adapter.got.Messages[1].ToolCalls[0], wantCall)
	}
	wantMessage := domain.Message{Role: domain.RoleTool, Text: "boom", ToolName: "lookup", ToolID: "c0", IsError: true, Offload: true}
	if !reflect.DeepEqual(adapter.got.Messages[2], wantMessage) {
		t.Fatalf("replayed tool result = %+v, want %+v", adapter.got.Messages[2], wantMessage)
	}

	var gotAgent, gotRun, gotSource string
	var ledgerCost *int64
	if err := w.pool.QueryRow(context.Background(), `
		SELECT agent_id::text, run_id::text, source, cost_micro_usd
		FROM gateway.ledger WHERE run_id = $1::uuid`, runID).Scan(&gotAgent, &gotRun, &gotSource, &ledgerCost); err != nil {
		t.Fatal(err)
	}
	if gotAgent != agentID || gotRun != runID || gotSource != "chat" {
		t.Fatalf("ledger attribution = %s %s %s", gotAgent, gotRun, gotSource)
	}

	if !slices.Equal(deltas, []string{"a ", "b "}) || resp.Text != "a b " {
		t.Fatalf("deltas = %v text = %q", deltas, resp.Text)
	}
	if len(resp.ToolCalls) != 2 || resp.ToolCalls[0].ID != "t1" || resp.ToolCalls[1].ID != "t2" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	wantUsage := kernel.TokenUsage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 25, CacheWriteTokens: 10, ReasoningTokens: 5}
	if resp.Usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", resp.Usage, wantUsage)
	}
	if resp.CostMicroUSD == nil || ledgerCost == nil || *resp.CostMicroUSD != *ledgerCost {
		t.Fatalf("cost = %v, ledger = %v", resp.CostMicroUSD, ledgerCost)
	}
}

func TestFeature_UPM310a_ChatTurnsRefusedBySpendLimitBeforeTheAdapter(t *testing.T) {
	w, adapter, client := chatTurnsWorld(t)
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10)
	model := chatgateway.Turns{Client: client}.Bind(chatapp.TurnRoute{Route: "pin-house", ChatID: "018f3b2c-7a10-7c4a-8e11-0000000000c3"}, nil, nil)
	_, err := model.Complete(context.Background(), agentloop.PromptRequest{
		UserID:   pinAuthor,
		Messages: []agentloop.Message{{Role: agentloop.RoleUser, Text: "q"}},
	})
	var modelErr *chatapp.ModelError
	if !errors.As(err, &modelErr) {
		t.Fatalf("err = %v", err)
	}
	if modelErr.Class != "limit_reached" || modelErr.Model != "" {
		t.Fatalf("model error = %+v", modelErr)
	}
	if adapter.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", adapter.calls)
	}
}

func TestFeature_UPM414_CoreToolsSendRetrieveOutputPagingSchemaToProvider(t *testing.T) {
	w := newPinWorld(t, nil)
	seedFakeVendorRate(t, &w, 75.0)
	if _, err := w.pool.Exec(context.Background(), `UPDATE gateway.provider_accounts SET base_url = 'https://api.openai.com' WHERE id = $1::uuid`, pinHouse); err != nil {
		t.Fatal(err)
	}

	var providerBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		providerBody = body
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(rw,
			"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	t.Cleanup(upstream.Close)

	transport := endpointRoundTrip(func(req *http.Request) (*http.Response, error) {
		moved := req.Clone(req.Context())
		moved.URL.Scheme = "http"
		moved.URL.Host = strings.TrimPrefix(upstream.URL, "http://")
		moved.Host = ""
		return http.DefaultTransport.RoundTrip(moved)
	})
	w.disp.Stream[domain.AdapterOpenAI] = openaiadapter.Adapter{Client: &http.Client{Transport: transport}}

	mux := http.NewServeMux()
	httpapi.Handler{Dispatch: w.disp}.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	secret := "upmo_svc_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	client, err := chatgateway.Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	model := chatgateway.Turns{Client: client}.Bind(chatapp.TurnRoute{
		Route:  "pin-house",
		ChatID: "018f3b2c-7a10-7c4a-8e11-0000000000c4",
	}, nil, nil)
	if _, err := model.Complete(context.Background(), agentloop.PromptRequest{
		UserID:   pinAuthor,
		AgentID:  "018f3b2c-7a10-7c4a-8e11-0000000000a4",
		RunID:    "018f3b2c-7a10-7c4a-8e11-0000000000b4",
		Messages: []agentloop.Message{{Role: agentloop.RoleUser, Text: "page it"}},
	}); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(providerBody, &payload); err != nil {
		t.Fatalf("unmarshal provider body: %v (%s)", err, providerBody)
	}
	wantCore := agentloop.CoreTools()
	if len(payload.Tools) != len(wantCore) {
		t.Fatalf("tools sent = %d, want %d (%s)", len(payload.Tools), len(wantCore), providerBody)
	}
	gotRetrieve := payload.Tools[0].Function
	if payload.Tools[0].Type != "function" || gotRetrieve.Name != "retrieve_output" || gotRetrieve.Description != wantCore[0].Description {
		t.Fatalf("retrieve_output tool = %+v", payload.Tools[0])
	}
	var params struct {
		Type       string         `json:"type"`
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(gotRetrieve.Parameters, &params); err != nil {
		t.Fatal(err)
	}
	wantProps := map[string]any{
		"id":     map[string]any{"type": "string"},
		"offset": map[string]any{"type": "integer", "minimum": float64(0)},
		"limit":  map[string]any{"type": "integer", "minimum": float64(1)},
	}
	if params.Type != "object" || !reflect.DeepEqual(params.Required, []string{"id"}) || !reflect.DeepEqual(params.Properties, wantProps) {
		t.Fatalf("retrieve_output parameters = %s", gotRetrieve.Parameters)
	}
}
