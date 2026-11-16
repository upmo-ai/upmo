package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

type rpcRoutes struct{ r domain.Route }

func (m rpcRoutes) ByName(context.Context, string) (domain.Route, error) { return m.r, nil }

type rpcSecrets map[string]string

func (m rpcSecrets) Get(ref string) (string, error) { return m[ref], nil }

type rpcPrices struct{ p domain.Price }

func (m rpcPrices) Price(context.Context, string) (domain.Price, bool, error) { return m.p, true, nil }

type rpcAdapter struct {
	lastReq domain.Request
}

func (a *rpcAdapter) Decide(context.Context, app.Attempt, domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error) {
	return domain.DecideResult{YesNo: true}, domain.Usage{
		Arrived: true, Input: 100, Output: 50, CacheRead: 25, CacheWrite: 10, Reasoning: 5,
	}, domain.ClassOK, nil
}

func (a *rpcAdapter) Chat(_ context.Context, attempt app.Attempt, req domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	a.lastReq = req
	return domain.ChatResult{
		Text:       "calling",
		ToolCalls:  []domain.ToolCall{{ID: "c1", Name: "search", Arguments: json.RawMessage(`{"q":"test"}`)}},
		StopReason: "tool_calls",
		Usage:      domain.Usage{Arrived: true, Input: 200, Output: 100, CacheRead: 50, CacheWrite: 20, Reasoning: 10},
		Model:      attempt.Model,
	}, domain.ClassOK, nil
}

func (a *rpcAdapter) ChatStream(context.Context, app.Attempt, domain.Request, func(string) error) (domain.Usage, domain.FailureClass, error) {
	return domain.Usage{}, domain.ClassOK, nil
}

func (a *rpcAdapter) ChatStreamTools(_ context.Context, attempt app.Attempt, req domain.Request, events app.StreamEvents) (domain.ChatResult, domain.FailureClass, error) {
	a.lastReq = req
	if events.Text != nil {
		if err := events.Text("calling"); err != nil {
			return domain.ChatResult{}, domain.ClassCancelled, err
		}
	}
	if events.ToolCall != nil {
		if err := events.ToolCall(app.ToolCallDelta{Index: 0, ID: "c1", Name: "search"}); err != nil {
			return domain.ChatResult{}, domain.ClassCancelled, err
		}
		if err := events.ToolCall(app.ToolCallDelta{Index: 0, ArgumentsDelta: `{"q":"test"}`}); err != nil {
			return domain.ChatResult{}, domain.ClassCancelled, err
		}
	}
	return domain.ChatResult{
		Text:       "calling",
		ToolCalls:  []domain.ToolCall{{ID: "c1", Name: "search", Arguments: json.RawMessage(`{"q":"test"}`)}},
		StopReason: "tool_calls",
		Usage:      domain.Usage{Arrived: true, Input: 200, Output: 100, CacheRead: 50, CacheWrite: 20, Reasoning: 10},
		Model:      attempt.Model,
	}, domain.ClassOK, nil
}

func TestHandlerChatChatStreamAndDecideMetadataAndTools(t *testing.T) {
	rate := 1.0
	adapter := &rpcAdapter{}
	acct := domain.Account{ID: "1", Adapter: "rpc", CredentialRef: "house", House: true, PlanKind: domain.PlanMetered}
	mux := http.NewServeMux()
	Handler{
		Dispatch: app.Dispatch{
			Routes:  rpcRoutes{domain.Route{Name: "chat", Capability: domain.CapChat, Links: []domain.Link{{Position: 1, Account: acct, Model: "m1"}}}},
			Secrets: rpcSecrets{"house": "secret"},
			Prices:  rpcPrices{domain.Price{Version: "v1", InputPerMTok: &rate, OutputPerMTok: &rate, CacheReadMTok: &rate, CacheWriteMTok: &rate, ContextWindow: 256000}},
			Chat:    map[string]app.Chatter{"rpc": adapter},
			Stream:  map[string]app.StreamChatter{"rpc": adapter},
		},
	}.Mount(mux)

	chatBody := `{
		"route":"chat",
		"max_output_tokens":333,
		"tool_choice":{"mode":"tool","name":"search"},
		"tools":[{"name":"search","schema":{"type":"object"}}],
		"messages":[
			{"role":"user","text":"find it"},
			{"role":"assistant","tool_calls":[{"id":"c0","name":"search","arguments":{"q":"x"}}]},
			{"role":"tool","tool_id":"c0","tool_name":"search","text":"err","is_error":true}
		]
	}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, PathChat, strings.NewReader(chatBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", rec.Code, rec.Body.String())
	}
	if adapter.lastReq.MaxOutputTokens != 333 || adapter.lastReq.ToolChoice.Name != "search" || !adapter.lastReq.Messages[2].IsError {
		t.Fatalf("adapter req = %+v", adapter.lastReq)
	}
	var gotChat chatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &gotChat); err != nil {
		t.Fatal(err)
	}
	if gotChat.Model != "m1" || gotChat.ContextWindow != 256000 || gotChat.CostMicroUSD == nil || *gotChat.CostMicroUSD != 370 {
		t.Fatalf("chat resp = %+v", gotChat)
	}
	if len(gotChat.ToolCalls) != 1 || gotChat.Usage.CacheRead != 50 || gotChat.Usage.CacheWrite != 20 || gotChat.Usage.Reasoning != 10 {
		t.Fatalf("chat usage/tools = %+v", gotChat)
	}

	// ChatStream
	srec := httptest.NewRecorder()
	mux.ServeHTTP(srec, httptest.NewRequest(http.MethodPost, PathChatStream, strings.NewReader(chatBody)))
	if srec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", srec.Code, srec.Body.String())
	}
	var toolDeltas int
	var done *chatStreamResult
	scan := bufio.NewScanner(srec.Body)
	for scan.Scan() {
		var f struct {
			ToolCall *app.ToolCallDelta `json:"tool_call"`
			Done     *chatStreamResult  `json:"done"`
		}
		if err := json.Unmarshal(scan.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		if f.ToolCall != nil {
			toolDeltas++
		}
		if f.Done != nil {
			done = f.Done
		}
	}
	if toolDeltas != 2 || done == nil || done.ContextWindow != 256000 || done.CostMicroUSD == nil || *done.CostMicroUSD != 370 {
		t.Fatalf("stream toolDeltas=%d done=%+v", toolDeltas, done)
	}
	if done.CacheRead != 50 || done.CacheWrite != 20 || done.Reasoning != 10 || !done.Arrived || len(done.ToolCalls) != 1 {
		t.Fatalf("stream done metadata = %+v", done)
	}

	// Decide
	dmux := http.NewServeMux()
	Handler{
		Dispatch: app.Dispatch{
			Routes:  rpcRoutes{domain.Route{Name: "dec", Capability: domain.CapDecide, Decision: domain.DecisionYesNo, Links: []domain.Link{{Position: 1, Account: acct, Model: "m1"}}}},
			Secrets: rpcSecrets{"house": "secret"},
			Prices:  rpcPrices{domain.Price{Version: "v1", InputPerMTok: &rate, OutputPerMTok: &rate, CacheReadMTok: &rate, CacheWriteMTok: &rate, ContextWindow: 256000}},
			Decide:  map[string]app.Decider{"rpc": adapter},
		},
	}.Mount(dmux)
	drec := httptest.NewRecorder()
	dmux.ServeHTTP(drec, httptest.NewRequest(http.MethodPost, PathDecide, strings.NewReader(`{"route":"dec","kind":"yesno","prompt":"ok?"}`)))
	if drec.Code != http.StatusOK {
		t.Fatalf("decide status=%d body=%s", drec.Code, drec.Body.String())
	}
	var gotDec decideResponse
	if err := json.Unmarshal(drec.Body.Bytes(), &gotDec); err != nil {
		t.Fatal(err)
	}
	if !gotDec.YesNo || !gotDec.Usage.Arrived || gotDec.Usage.CacheRead != 25 || gotDec.Usage.CacheWrite != 10 || gotDec.Usage.Reasoning != 5 || gotDec.Usage.ContextWindow != 256000 || gotDec.Usage.CostMicroUSD == nil || *gotDec.Usage.CostMicroUSD != 185 {
		t.Fatalf("decide resp = %+v", gotDec)
	}
}
