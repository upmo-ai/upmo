package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIHeadersValues(t *testing.T) {
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=(), payment=()",
	}
	plain := APIHeaders(false)
	for name, value := range want {
		if got := plain.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if got := plain.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain HSTS = %q, want none", got)
	}
	secure := APIHeaders(true)
	if got := secure.Get("Strict-Transport-Security"); got != "max-age=15552000" {
		t.Errorf("secure HSTS = %q", got)
	}
	if len(secure) != len(plain)+1 {
		t.Errorf("secure has %d headers, plain %d; only HSTS may differ", len(secure), len(plain))
	}
}

func TestSecurityHeadersCacheControl(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stream" {
			w.Header().Set("Cache-Control", "no-cache, no-store")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	for path, want := range map[string]string{
		"/api/session": "no-store",
		"/api/stream":  "no-cache, no-store",
		"/healthz":     "",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s Cache-Control = %q, want %q", path, got, want)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s lost nosniff", path)
		}
	}
}
