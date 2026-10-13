package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func TestAdapterDecideYesNo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer s" {
			t.Fatalf("auth = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "yes"}}},
		})
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	got, _, class, err := a.Decide(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err != nil || class != domain.ClassOK || !got.YesNo {
		t.Fatalf("got=%v class=%s err=%v", got, class, err)
	}
}

func TestAdapterMapsStatusToClass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	_, _, class, err := a.Chat(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err == nil || class != domain.ClassCredential {
		t.Fatalf("class=%s err=%v", class, err)
	}
}

func TestAdapterEmbedOrdersByIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer s" {
			t.Fatalf("auth = %q", got)
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "m" || len(body.Input) != 2 || body.Input[0] != "one" {
			t.Fatalf("body = %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{0, 1}},
				{"index": 0, "embedding": []float32{1, 0}},
			},
			"usage": map[string]int{"prompt_tokens": 4},
		})
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	got, usage, class, err := a.Embed(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, []string{"one", "two"})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if len(got) != 2 || got[0][0] != 1 || got[1][1] != 1 || !usage.Arrived || usage.Input != 4 {
		t.Fatalf("got=%v usage=%+v", got, usage)
	}
}

func TestEncodeChatOrderIsToolsSystemMessages(t *testing.T) {
	req, err := domain.Build(domain.Input{
		Tools:    []domain.Tool{{Name: "search", Schema: json.RawMessage(`{"type":"object"}`)}},
		System:   "be brief",
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeChat("m", req)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	toolsAt := strings.Index(text, `"tools"`)
	systemAt := strings.Index(text, `"system"`)
	messagesAt := strings.Index(text, `"messages"`)
	if toolsAt < 0 || systemAt < 0 || messagesAt < 0 || toolsAt >= systemAt || systemAt >= messagesAt {
		t.Fatalf("order = %s", text)
	}
	if !strings.Contains(text, `"cache_control"`) {
		t.Fatalf("missing cache breakpoint: %s", text)
	}
}
