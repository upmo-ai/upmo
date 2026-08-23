package mcpauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransportStepUpOnInsufficientScope(t *testing.T) {
	var scopes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer tok-write" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="files:write"`)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	tr := &Transport{
		Base:     srv.Client().Transport,
		Resource: "https://mcp.example.com/mcp",
		Scope:    "files:read",
		Token: func(_ context.Context, _, scope string) (string, error) {
			scopes = append(scopes, scope)
			if strings.Contains(scope, "files:write") {
				return "tok-write", nil
			}
			return "tok-read", nil
		},
	}
	cli := &http.Client{Transport: tr}
	res, err := cli.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status %d body %s", res.StatusCode, body)
	}
	if len(scopes) < 2 || scopes[1] != "files:read files:write" {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestTransportRefusesPassthrough(t *testing.T) {
	trap := &trapRT{}
	tr := &Transport{Base: trap, Token: func(context.Context, string, string) (string, error) {
		return "upmo_pat_abc", nil
	}}
	req, _ := http.NewRequest(http.MethodGet, "https://mcp.example.com/mcp", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("accepted passthrough")
	}
	if trap.called {
		t.Fatal("dialed with an Upmo token")
	}
}

func TestTransportStopsWhenScopesDoNotGrow(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="files:read"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	tr := &Transport{
		Base:     srv.Client().Transport,
		Resource: "https://mcp.example.com/mcp",
		Scope:    "files:read",
		Token:    func(context.Context, string, string) (string, error) { return "tok", nil },
	}
	cli := &http.Client{Transport: tr}
	res, err := cli.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if n != 1 {
		t.Fatalf("calls = %d", n)
	}
}

type trapRT struct{ called bool }

func (t *trapRT) RoundTrip(*http.Request) (*http.Response, error) {
	t.called = true
	return nil, fmt.Errorf("should not dial")
}

func FuzzRefusePassthrough(f *testing.F) {
	f.Add("upmo_pat_abc")
	f.Add("upmo_s2s_v1.x")
	f.Add("Bearer upmo_svc_Z")
	f.Add("mcp_ok")
	f.Fuzz(func(t *testing.T, token string) {
		_ = RefusePassthrough(token)
		if strings.HasPrefix(token, "upmo_pat_") || strings.HasPrefix(token, "upmo_s2s_") || strings.HasPrefix(token, "upmo_svc_") {
			if err := RefusePassthrough(token); err == nil {
				t.Fatalf("accepted %q", token)
			}
		}
	})
}
