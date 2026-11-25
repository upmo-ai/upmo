package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

func TestCallEmbed(t *testing.T) {
	secret := serviceKey(t, 'k')
	var gotPath, gotUser, gotRoute string
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
		if r.URL.Path != pathEmbed {
			http.NotFound(w, r)
			return
		}
		raw, _ := body["inputs"].([]any)
		for _, item := range raw {
			text, _ := item.(string)
			gotInputs = append(gotInputs, text)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "model-a",
			"vectors": [][]float64{{1, 0}, {0, 1}},
		})
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret, "knowledge-embed")
	if err != nil {
		t.Fatal(err)
	}
	model, vectors, err := client.For("018f3b2c-7a10-7c4a-8e11-111111111111").Embed(
		context.Background(), []string{"Ada prefers tea", "Ada prefers coffee"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != pathEmbed || gotRoute != "knowledge-embed" || gotUser == "" {
		t.Fatalf("path=%s route=%s user=%s", gotPath, gotRoute, gotUser)
	}
	if model != "model-a" || len(vectors) != 2 || len(vectors[0]) != 2 || vectors[1][1] != 1 || len(gotInputs) != 2 {
		t.Fatalf("model=%s vectors=%v inputs=%v", model, vectors, gotInputs)
	}
}

func TestCallEmbedGatewayError(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret, "knowledge-embed")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.For("person").Embed(context.Background(), []string{"Ada prefers tea"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestOpenRejectsRelativeURL(t *testing.T) {
	if _, err := Open("gateway:8080", serviceKey(t, 'k'), "knowledge-embed"); err == nil {
		t.Fatal("relative url")
	}
}

func TestOpenRejectsEmptyRoute(t *testing.T) {
	if _, err := Open("http://gateway:8080", serviceKey(t, 'k'), ""); err == nil {
		t.Fatal("empty route")
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
