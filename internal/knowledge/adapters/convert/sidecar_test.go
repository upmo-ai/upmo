package convert_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/upmo-ai/upmo/internal/knowledge/adapters/convert"
	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// TestOpenSidecarSignsForMarkitdown runs brain's sidecar client against a
// listener that verifies the markitdown v2 bearer over the body it gets.
func TestOpenSidecarSignsForMarkitdown(t *testing.T) {
	secret := svcsig.KeyPrefix + "markitdown_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'d'}, 32))
	key, err := svcsig.ParseAudienceKey(secret, svcsig.AudienceMarkitdown)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	h, err := svcsig.Handler([][]byte{key}, svcsig.AudienceMarkitdown, 32<<20, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"markdown":"# doc"}`))
	}))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sidecar, err := convert.OpenSidecar(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	md, err := sidecar.ToMarkdown(context.Background(), "application/pdf", []byte("%PDF-1.7"))
	if err != nil || md != "# doc" || string(got) != "%PDF-1.7" {
		t.Fatalf("markdown = %q %v, body = %q", md, err, got)
	}
	unsigned := &convert.Sidecar{BaseURL: srv.URL}
	if _, err := unsigned.ToMarkdown(context.Background(), "application/pdf", []byte("%PDF-1.7")); !errors.Is(err, app.ErrConvert) {
		t.Fatalf("unsigned call = %v, want the sidecar to refuse it", err)
	}
	if _, err := convert.OpenSidecar(srv.URL, svcsig.KeyPrefix+"gateway_"+base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'d'}, 32))); err == nil {
		t.Fatal("opened the sidecar with a gateway key")
	}
}

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
