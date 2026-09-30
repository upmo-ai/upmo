package sandboxd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime/app"
)

func TestClientCreateAndDestroySigned(t *testing.T) {
	secret := serviceKey(t, 's')
	var paths []string
	var created map[string]any
	srv := httptest.NewServer(mustSandboxd(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+svcsig.BearerPrefix+svcsig.AudienceSandboxd+".") {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case pathCreate:
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "box-1",
				"tier":    "container",
				"shape":   map[string]any{"scratch": "scratch", "cpu": 1, "burst": 2, "pids": 256, "exec_seconds": 900},
				"expires": time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
			})
		case pathDestroy:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["id"] != "box-1" {
				t.Errorf("destroy body = %v %v", body, err)
			}
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	})))
	t.Cleanup(srv.Close)

	client, err := Open(srv.URL+"/", secret)
	if err != nil {
		t.Fatal(err)
	}
	box, err := client.Create(context.Background(), app.Spec{
		Task:        "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image:       "upmo/sandbox:base",
		Tier:        "container",
		TTL:         time.Hour,
		Allow:       []string{"github.com"},
		Credentials: []app.Credential{{Sentinel: "upmo_sentinel_gh", Ref: "env:GITHUB_TOKEN", Host: "github.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if box.ID != "box-1" || box.Shape.Exec != 15*time.Minute || box.Shape.PIDs != 256 || box.Expires.IsZero() {
		t.Fatalf("box = %+v", box)
	}
	if created["ttl_seconds"] != float64(3600) || created["tier"] != "container" {
		t.Fatalf("create body = %v", created)
	}
	creds, _ := created["credentials"].([]any)
	if len(creds) != 1 || creds[0].(map[string]any)["ref"] != "env:GITHUB_TOKEN" {
		t.Fatalf("credentials = %v", created["credentials"])
	}
	if err := client.Destroy(context.Background(), box.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != pathCreate+","+pathDestroy {
		t.Fatalf("paths = %v", paths)
	}
}

func TestClientWrongKeyIsRefused(t *testing.T) {
	srv := httptest.NewServer(mustSandboxd(t, serviceKey(t, 's'), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("inner handler reached with the wrong key")
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, serviceKey(t, 'x'))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Destroy(context.Background(), "box-1")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401", err)
	}
}

func TestClientMapsStatus(t *testing.T) {
	secret := serviceKey(t, 's')
	status := map[string]int{
		pathCreate:  http.StatusConflict,
		pathPause:   http.StatusNotFound,
		pathDestroy: http.StatusBadRequest,
		pathExec:    http.StatusNotImplemented,
		pathPTY:     http.StatusNotImplemented,
		pathExpose:  http.StatusOK,
	}
	srv := httptest.NewServer(mustSandboxd(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, ok := status[r.URL.Path]
		if !ok {
			code = http.StatusInternalServerError
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte("{}"))
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.Create(ctx, app.Spec{}); !errors.Is(err, app.ErrIsolation) {
		t.Errorf("create err = %v", err)
	}
	if err := client.Pause(ctx, "gone"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("pause err = %v", err)
	}
	if err := client.Destroy(ctx, ""); !errors.Is(err, app.ErrSpec) {
		t.Errorf("destroy err = %v", err)
	}
	if _, err := client.Exec(ctx, "box", app.Command{Args: []string{"true"}}, nil); !errors.Is(err, app.ErrUnsupported) {
		t.Errorf("exec err = %v", err)
	}
	if _, err := client.PTY(ctx, "box", app.Size{Rows: 24, Cols: 80}); !errors.Is(err, app.ErrUnsupported) {
		t.Errorf("pty err = %v", err)
	}
	if _, err := client.Expose(ctx, "box", 3000); !errors.Is(err, app.ErrUnsupported) {
		t.Errorf("expose err = %v", err)
	}
	if _, err := client.ReadFile(ctx, "box", "a.txt"); err == nil || errors.Is(err, app.ErrUnsupported) {
		t.Errorf("read err = %v, want a 500", err)
	}
}

func TestOpenRefuses(t *testing.T) {
	if _, err := Open("sandboxd:8083", serviceKey(t, 's')); err == nil {
		t.Error("relative url")
	}
	if _, err := Open("http://sandboxd:8083", ""); err == nil {
		t.Error("no key")
	}
	if _, err := Open("http://sandboxd:8083", "upmo_pat_abc"); err == nil {
		t.Error("personal token as key")
	}
}

func mustSandboxd(t *testing.T, secret string, inner http.Handler) http.Handler {
	t.Helper()
	master, err := svcsig.ParseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceSandboxd)
	if err != nil {
		t.Fatal(err)
	}
	h, err := svcsig.Handler([][]byte{derived}, svcsig.AudienceSandboxd, inner)
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
	return svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
}
