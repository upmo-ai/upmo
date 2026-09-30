package sandboxd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime/app"
	"github.com/upmo-ai/upmo/internal/runtime/domain"
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
	if _, err := client.ReadFile(ctx, "box", "a.txt"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("read err = %v, want a 500", err)
	}
}

// signed starts a sandboxd stand-in behind the HMAC guard.
func signed(t *testing.T, h http.HandlerFunc) Client {
	t.Helper()
	secret := serviceKey(t, 's')
	srv := httptest.NewServer(mustSandboxd(t, secret, h))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// ndjson writes frames as an Exec stream.
func ndjson(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", contentTypeStream)
	w.WriteHeader(http.StatusOK)
	for _, l := range lines {
		_, _ = w.Write([]byte(l + "\n"))
		w.(http.Flusher).Flush()
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestClientExecStreams(t *testing.T) {
	var sent execBody
	client := signed(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathExec {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		ndjson(w,
			`{"type":"output","stream":"stdout","data":"`+b64("out\n")+`"}`,
			`{"type":"output","stream":"stderr","data":"`+b64("err\n")+`"}`,
			`{"type":"output","stream":"stdout","data":""}`,
			`{"type":"exit","code":3}`,
		)
	})
	var got []app.Output
	exit, err := client.Exec(context.Background(), "box-1",
		app.Command{Args: []string{"sh", "-c", "echo out"}, Dir: "/scratch", Timeout: 1500 * time.Microsecond},
		func(o app.Output) error {
			got = append(got, o)
			return nil
		})
	if err != nil || exit.Code != 3 {
		t.Fatalf("exec = %+v %v", exit, err)
	}
	if len(got) != 2 || got[0].Stream != "stdout" || string(got[0].Data) != "out\n" || got[1].Stream != "stderr" || string(got[1].Data) != "err\n" {
		t.Fatalf("output = %+v", got)
	}
	// 1.5ms rounds up, so a short timeout never reads as zero, the cap.
	if sent.ID != "box-1" || !slices.Equal(sent.Args, []string{"sh", "-c", "echo out"}) || sent.Dir != "/scratch" || sent.TimeoutMS != 2 {
		t.Fatalf("sent = %+v", sent)
	}

	// No callback still reads to the exit.
	if exit, err := client.Exec(context.Background(), "box-1", app.Command{Args: []string{"true"}}, nil); err != nil || exit.Code != 3 {
		t.Fatalf("exec without out = %+v %v", exit, err)
	}
	if sent.TimeoutMS != 0 {
		t.Fatalf("no timeout sent %d", sent.TimeoutMS)
	}
}

func TestClientExecErrors(t *testing.T) {
	var reply func(w http.ResponseWriter)
	client := signed(t, func(w http.ResponseWriter, _ *http.Request) { reply(w) })
	ctx := context.Background()
	cmd := app.Command{Args: []string{"true"}}
	cases := map[string]struct {
		reply func(w http.ResponseWriter)
		check func(error) bool
	}{
		"timeout frame": {
			func(w http.ResponseWriter) {
				ndjson(w, `{"type":"output","stream":"stdout","data":"`+b64("x")+`"}`, `{"type":"error","error":"timeout"}`)
			},
			func(err error) bool { return errors.Is(err, context.DeadlineExceeded) },
		},
		"unavailable frame": {
			func(w http.ResponseWriter) { ndjson(w, `{"type":"error","error":"unavailable"}`) },
			func(err error) bool { return err != nil && strings.Contains(err.Error(), "exec failed") },
		},
		"cut short": {
			func(w http.ResponseWriter) { ndjson(w, `{"type":"output","stream":"stdout","data":"`+b64("x")+`"}`) },
			func(err error) bool { return errors.Is(err, errCutShort) },
		},
		"exit without code": {
			func(w http.ResponseWriter) { ndjson(w, `{"type":"exit"}`) },
			func(err error) bool { return errors.Is(err, errBody) },
		},
		"unknown frame": {
			func(w http.ResponseWriter) { ndjson(w, `{"type":"stdin"}`) },
			func(err error) bool { return errors.Is(err, errBody) },
		},
		"unknown stream": {
			func(w http.ResponseWriter) { ndjson(w, `{"type":"output","stream":"stdin","data":"eA=="}`) },
			func(err error) bool { return errors.Is(err, errBody) },
		},
		"not json": {
			func(w http.ResponseWriter) { ndjson(w, `nope`) },
			func(err error) bool { return errors.Is(err, errBody) },
		},
		"line too long": {
			func(w http.ResponseWriter) { ndjson(w, strings.Repeat("x", bodyLimit+1)) },
			func(err error) bool { return err != nil && !errors.Is(err, errCutShort) },
		},
		"not a stream": {
			func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) },
			func(err error) bool { return errors.Is(err, errBody) },
		},
		"paused": {
			func(w http.ResponseWriter) {
				w.Header().Set(headerError, "paused")
				http.Error(w, "sandbox is paused", http.StatusConflict)
			},
			func(err error) bool { return errors.Is(err, app.ErrPaused) && !errors.Is(err, app.ErrIsolation) },
		},
		"isolation by status": {
			func(w http.ResponseWriter) { http.Error(w, "conflict", http.StatusConflict) },
			func(err error) bool { return errors.Is(err, app.ErrIsolation) },
		},
		"unknown sandbox": {
			func(w http.ResponseWriter) {
				w.Header().Set(headerError, "not_found")
				http.Error(w, "not found", http.StatusNotFound)
			},
			func(err error) bool { return errors.Is(err, app.ErrNotFound) },
		},
	}
	for name, c := range cases {
		reply = c.reply
		if _, err := client.Exec(ctx, "box", cmd, nil); !c.check(err) {
			t.Errorf("%s err = %v", name, err)
		}
	}
}

func TestClientExecStopsWhenOutFails(t *testing.T) {
	left := make(chan struct{})
	client := signed(t, func(w http.ResponseWriter, r *http.Request) {
		ndjson(w, `{"type":"output","stream":"stdout","data":"`+b64("x")+`"}`)
		<-r.Context().Done()
		close(left)
	})
	stop := errors.New("enough")
	_, err := client.Exec(context.Background(), "box", app.Command{Args: []string{"yes"}}, func(app.Output) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-left:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed open after out failed")
	}
}

func TestClientExecRefusesTimeoutsLocally(t *testing.T) {
	client := signed(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused timeout reached sandboxd")
	})
	for _, timeout := range []time.Duration{-time.Second, domain.ResourceShape().Exec + time.Millisecond} {
		if _, err := client.Exec(context.Background(), "box", app.Command{Args: []string{"true"}, Timeout: timeout}, nil); !errors.Is(err, app.ErrSpec) {
			t.Errorf("timeout %v err = %v", timeout, err)
		}
	}
}

func TestExecBudget(t *testing.T) {
	limit := domain.ResourceShape().Exec
	if got := execBudget(0); got != limit+execGrace {
		t.Errorf("no timeout = %v", got)
	}
	if got := execBudget(time.Second); got != time.Second+execGrace {
		t.Errorf("one second = %v", got)
	}
	// Exec has no client timeout, so a long command is bounded only by its budget.
	client, err := Open("http://sandboxd:8083", serviceKey(t, 's'))
	if err != nil {
		t.Fatal(err)
	}
	if client.stream.Timeout != 0 || client.http.Timeout != unaryTimeout {
		t.Fatalf("timeouts = %v %v", client.stream.Timeout, client.http.Timeout)
	}
}

func TestClientFiles(t *testing.T) {
	var wrote fileBody
	client := signed(t, func(w http.ResponseWriter, r *http.Request) {
		var body fileBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch {
		case r.URL.Path == pathWriteFile:
			wrote = body
			_, _ = w.Write([]byte(`{}`))
		case body.Path == "a.bin":
			_, _ = w.Write([]byte(`{"data":"` + base64.StdEncoding.EncodeToString([]byte("bin\x00\xff")) + `"}`))
		case body.Path == "empty":
			_, _ = w.Write([]byte(`{"data":""}`))
		case body.Path == "nodata":
			_, _ = w.Write([]byte(`{}`))
		case body.Path == "dir":
			w.Header().Set(headerError, "not_file")
			http.Error(w, "not a regular file", http.StatusUnprocessableEntity)
		case body.Path == "big":
			w.Header().Set(headerError, "too_large")
			http.Error(w, "file is too large", http.StatusRequestEntityTooLarge)
		default:
			w.Header().Set(headerError, "no_file")
			http.Error(w, "no such file", http.StatusNotFound)
		}
	})
	ctx := context.Background()

	if got, err := client.ReadFile(ctx, "box-1", "a.bin"); err != nil || string(got) != "bin\x00\xff" {
		t.Fatalf("read = %q %v", got, err)
	}
	if got, err := client.ReadFile(ctx, "box-1", "empty"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty read = %#v %v", got, err)
	}
	if _, err := client.ReadFile(ctx, "box-1", "nodata"); !errors.Is(err, errBody) {
		t.Fatalf("no data err = %v", err)
	}
	if _, err := client.ReadFile(ctx, "box-1", "missing"); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := client.ReadFile(ctx, "box-1", "dir"); !errors.Is(err, app.ErrNotFile) {
		t.Fatalf("dir err = %v", err)
	}
	if _, err := client.ReadFile(ctx, "box-1", "big"); !errors.Is(err, app.ErrTooLarge) {
		t.Fatalf("big err = %v", err)
	}

	if err := client.WriteFile(ctx, "box-1", "notes/a.txt", []byte("hi\x00")); err != nil {
		t.Fatal(err)
	}
	if wrote.ID != "box-1" || wrote.Path != "notes/a.txt" || string(wrote.Data) != "hi\x00" {
		t.Fatalf("wrote = %+v", wrote)
	}
	wrote = fileBody{}
	if err := client.WriteFile(ctx, "box-1", "big", make([]byte, app.MaxFile+1)); !errors.Is(err, app.ErrTooLarge) {
		t.Fatalf("large write err = %v", err)
	}
	if wrote.Path != "" {
		t.Fatal("an oversized write travelled")
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
