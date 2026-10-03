package convert_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/upmo-ai/upmo/internal/knowledge/adapters/convert"
	"github.com/upmo-ai/upmo/internal/knowledge/app"
)

func TestClientSkipsSidecarForMarkdown(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)
	c := convert.Client{Sidecar: &convert.Sidecar{BaseURL: srv.URL}}
	got, err := c.ToMarkdown(context.Background(), "text/markdown", []byte("hello"))
	if err != nil || got != "hello" || calls != 0 {
		t.Fatalf("got %q err %v calls %d", got, err, calls)
	}
}

func TestClientCallsSidecarForPDF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/convert" || r.Header.Get("X-Mimetype") != "application/pdf" {
			t.Fatalf("path %s mime %s", r.URL.Path, r.Header.Get("X-Mimetype"))
		}
		_, _ = w.Write([]byte(`{"markdown":"From PDF"}`))
	}))
	t.Cleanup(srv.Close)
	c := convert.Client{Sidecar: &convert.Sidecar{BaseURL: srv.URL}}
	got, err := c.ToMarkdown(context.Background(), "application/pdf", []byte("%PDF"))
	if err != nil || got != "From PDF" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestClientFailsWhenSidecarDown(t *testing.T) {
	c := convert.Client{Sidecar: &convert.Sidecar{BaseURL: "http://127.0.0.1:1"}}
	_, err := c.ToMarkdown(context.Background(), "application/pdf", []byte("%PDF"))
	if !errors.Is(err, app.ErrConvert) {
		t.Fatalf("err %v", err)
	}
}

func TestClientFailsWithoutSidecar(t *testing.T) {
	_, err := (convert.Client{}).ToMarkdown(context.Background(), "application/pdf", []byte("%PDF"))
	if !errors.Is(err, app.ErrConvert) {
		t.Fatalf("err %v", err)
	}
}
