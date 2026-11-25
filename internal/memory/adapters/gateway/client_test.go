package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

func TestCallProposeAndEmbed(t *testing.T) {
	secret := serviceKey(t, 'k')
	var gotPath, gotUser, gotRoute, gotPrompt string
	var gotInputs []string
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+svcsig.BearerPrefixV2+svcsig.AudienceGateway+".") {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		gotPath = r.URL.Path
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotUser, _ = body["user_id"].(string)
		gotRoute, _ = body["route"].(string)
		switch r.URL.Path {
		case pathChat:
			gotPrompt, _ = body["prompt"].(string)
			_ = json.NewEncoder(w).Encode(map[string]string{"text": `[{"claim":"Ada prefers tea","changes_behavior":true}]`})
		case pathEmbed:
			raw, _ := body["inputs"].([]any)
			for _, item := range raw {
				text, _ := item.(string)
				gotInputs = append(gotInputs, text)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":   "model-a",
				"vectors": [][]float64{{1, 0}, {0, 1}},
			})
		default:
			http.NotFound(w, r)
		}
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret, "memory-extract", "memory-embed")
	if err != nil {
		t.Fatal(err)
	}
	call := client.For("018f3b2c-7a10-7c4a-8e11-111111111111")
	text, err := call.Propose(context.Background(), "fenced")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != pathChat || gotRoute != "memory-extract" || gotUser == "" || gotPrompt != "fenced" || text == "" {
		t.Fatalf("path=%s route=%s user=%s prompt=%s text=%s", gotPath, gotRoute, gotUser, gotPrompt, text)
	}
	model, vectors, err := call.Embed(context.Background(), []string{"Ada prefers tea", "Ada prefers coffee"})
	if err != nil {
		t.Fatal(err)
	}
	if model != "model-a" || len(vectors) != 2 || len(vectors[0]) != 2 || vectors[1][1] != 1 || len(gotInputs) != 2 {
		t.Fatalf("model=%s vectors=%v inputs=%v", model, vectors, gotInputs)
	}
}

func TestCallRetriesGatewayOutage(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret, "memory-extract", "memory-embed")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.For("person").Propose(context.Background(), "fenced")
	if !errors.Is(err, app.ErrRetry) {
		t.Fatalf("err = %v", err)
	}
}

func TestCallDoesNotRetryCallerFault(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret, "memory-extract", "memory-embed")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.For("person").Embed(context.Background(), []string{"Ada prefers tea"})
	if err == nil || errors.Is(err, app.ErrRetry) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRejectsRelativeURL(t *testing.T) {
	if _, err := Open("gateway:8080", serviceKey(t, 'k'), "memory-extract", "memory-embed"); err == nil {
		t.Fatal("relative url")
	}
}

func mustGateway(t *testing.T, secret string, inner http.Handler) http.Handler {
	t.Helper()
	derived, err := svcsig.ParseAudienceKey(secret, svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	h, err := svcsig.Handler([][]byte{derived}, svcsig.AudienceGateway, 1<<20, inner)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serviceKey(t *testing.T, b byte) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return svcsig.KeyPrefix + "gateway_" + base64.RawStdEncoding.EncodeToString(raw)
}
