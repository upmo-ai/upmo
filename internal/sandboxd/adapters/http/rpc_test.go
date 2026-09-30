package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/stub"
)

// fake is the stub driver with scripted Exec, ReadFile and WriteFile.
type fake struct {
	*stub.Driver
	exec  func(ctx context.Context, id string, cmd runtime.Command, out func(runtime.Output) error) (runtime.Exit, error)
	read  func(ctx context.Context, id, path string) ([]byte, error)
	write func(ctx context.Context, id, path string, data []byte) error
}

func (f fake) Exec(ctx context.Context, id string, cmd runtime.Command, out func(runtime.Output) error) (runtime.Exit, error) {
	if f.exec == nil {
		return runtime.Exit{}, runtime.ErrUnsupported
	}
	return f.exec(ctx, id, cmd, out)
}

func (f fake) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	if f.read == nil {
		return nil, runtime.ErrUnsupported
	}
	return f.read(ctx, id, path)
}

func (f fake) WriteFile(ctx context.Context, id, path string, data []byte) error {
	if f.write == nil {
		return runtime.ErrUnsupported
	}
	return f.write(ctx, id, path, data)
}

func serve(t *testing.T) *http.ServeMux {
	t.Helper()
	return serveWith(t, fake{Driver: stub.New(nil)})
}

func serveWith(t *testing.T, p runtime.SandboxProvider) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	Handler{Provider: p}.Mount(mux)
	return mux
}

func post(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

// frames splits an Exec body into its frames.
func frames(t *testing.T, body []byte) []frame {
	t.Helper()
	var out []frame
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(nil, bodyLimit)
	for sc.Scan() {
		var f frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			t.Fatalf("frame %q: %v", sc.Text(), err)
		}
		out = append(out, f)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

const createJSON = `{"task_id":"018f3b2c-7a10-7c4a-8e11-111111111111","image":"upmo/sandbox:base","tier":"container","ttl_seconds":600,"allow":["github.com"]}`

func TestCreateThenDestroy(t *testing.T) {
	mux := serve(t)
	rec := post(mux, PathCreate, createJSON)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var box sandboxBody
	if err := json.Unmarshal(rec.Body.Bytes(), &box); err != nil {
		t.Fatal(err)
	}
	if box.ID == "" || box.Tier != "container" || box.Shape.ExecSeconds != 900 || box.Shape.PIDs != 256 {
		t.Fatalf("box = %+v", box)
	}
	if rec := post(mux, PathPause, `{"id":"`+box.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d", rec.Code)
	}
	if rec := post(mux, PathDestroy, `{"id":"`+box.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("destroy = %d", rec.Code)
	}
	rec = post(mux, PathDestroy, `{"id":"`+box.ID+`"}`)
	if rec.Code != http.StatusNotFound || rec.Header().Get(HeaderError) != CodeNotFound {
		t.Fatalf("second destroy = %d %q", rec.Code, rec.Header().Get(HeaderError))
	}
}

func TestCreateRefuses(t *testing.T) {
	mux := serve(t)
	cases := map[string]struct {
		body string
		code int
		wire string
	}{
		"not json":     {`{`, http.StatusBadRequest, CodeSpec},
		"no ttl":       {strings.Replace(createJSON, `"ttl_seconds":600`, `"ttl_seconds":0`, 1), http.StatusBadRequest, CodeSpec},
		"huge ttl":     {strings.Replace(createJSON, `"ttl_seconds":600`, `"ttl_seconds":9223372036854775807`, 1), http.StatusBadRequest, CodeSpec},
		"address":      {strings.Replace(createJSON, `"github.com"`, `"169.254.169.254"`, 1), http.StatusBadRequest, CodeSpec},
		"gvisor stub":  {strings.Replace(createJSON, `"container"`, `"gvisor"`, 1), http.StatusConflict, CodeIsolation},
		"unknown tier": {strings.Replace(createJSON, `"container"`, `"none"`, 1), http.StatusBadRequest, CodeSpec},
	}
	for name, c := range cases {
		rec := post(mux, PathCreate, c.body)
		if rec.Code != c.code || rec.Header().Get(HeaderError) != c.wire {
			t.Errorf("%s = %d %q %s, want %d %q", name, rec.Code, rec.Header().Get(HeaderError), rec.Body, c.code, c.wire)
		}
	}
}

func TestExecStreamsFrames(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 2*FrameData+1)
	var got runtime.Command
	mux := serveWith(t, fake{Driver: stub.New(nil), exec: func(_ context.Context, id string, cmd runtime.Command, out func(runtime.Output) error) (runtime.Exit, error) {
		if id != "box-1" {
			t.Errorf("id = %q", id)
		}
		got = cmd
		for _, o := range []runtime.Output{
			{Stream: "stdout", Data: []byte("out\n")},
			{Stream: "stderr", Data: []byte("err\n")},
			{Stream: "stdout"},
			{Stream: "stdout", Data: big},
		} {
			if err := out(o); err != nil {
				return runtime.Exit{}, err
			}
		}
		return runtime.Exit{Code: 3}, nil
	}})
	rec := post(mux, PathExec, `{"id":"box-1","args":["sh","-c","echo out"],"dir":"/scratch","timeout_ms":1500}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ContentTypeStream {
		t.Fatalf("exec = %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if !slices.Equal(got.Args, []string{"sh", "-c", "echo out"}) || got.Dir != "/scratch" || got.Timeout != 1500*time.Millisecond {
		t.Fatalf("cmd = %+v", got)
	}
	fr := frames(t, rec.Body.Bytes())
	if len(fr) != 6 {
		t.Fatalf("frames = %d: %s", len(fr), rec.Body)
	}
	if fr[0].Type != "output" || fr[0].Stream != "stdout" || string(fr[0].Data) != "out\n" {
		t.Errorf("frame 0 = %+v", fr[0])
	}
	if fr[1].Type != "output" || fr[1].Stream != "stderr" || string(fr[1].Data) != "err\n" {
		t.Errorf("frame 1 = %+v", fr[1])
	}
	var joined []byte
	for _, f := range fr[2:5] {
		if f.Type != "output" || f.Stream != "stdout" || len(f.Data) > FrameData {
			t.Errorf("split frame = %s %s %d", f.Type, f.Stream, len(f.Data))
		}
		joined = append(joined, f.Data...)
	}
	if !bytes.Equal(joined, big) {
		t.Errorf("split output is %d bytes, want %d", len(joined), len(big))
	}
	if last := fr[5]; last.Type != "exit" || last.Code == nil || *last.Code != 3 {
		t.Errorf("last frame = %+v", last)
	}
}

func TestExecWithoutOutputStillEndsWithExit(t *testing.T) {
	mux := serveWith(t, fake{Driver: stub.New(nil), exec: func(context.Context, string, runtime.Command, func(runtime.Output) error) (runtime.Exit, error) {
		return runtime.Exit{}, nil
	}})
	rec := post(mux, PathExec, `{"id":"box-1","args":["true"]}`)
	fr := frames(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || len(fr) != 1 || fr[0].Type != "exit" || fr[0].Code == nil || *fr[0].Code != 0 {
		t.Fatalf("exec = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"code":0`) {
		t.Fatalf("exit 0 is not on the wire: %s", rec.Body)
	}
}

func TestExecRefusesBeforeOutput(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		code   string
	}{
		"unknown": {runtime.ErrNotFound, http.StatusNotFound, CodeNotFound},
		"paused":  {fmt.Errorf("docker: %w", runtime.ErrPaused), http.StatusConflict, CodePaused},
		"spec":    {runtime.ErrSpec, http.StatusBadRequest, CodeSpec},
		"timeout": {fmt.Errorf("docker: command timed out: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, CodeTimeout},
		"stub":    {runtime.ErrUnsupported, http.StatusNotImplemented, CodeUnsupported},
		"engine":  {errors.New("engine is down"), http.StatusInternalServerError, CodeUnavailable},
	}
	for name, c := range cases {
		mux := serveWith(t, fake{Driver: stub.New(nil), exec: func(context.Context, string, runtime.Command, func(runtime.Output) error) (runtime.Exit, error) {
			return runtime.Exit{}, c.err
		}})
		rec := post(mux, PathExec, `{"id":"box-1","args":["true"]}`)
		if rec.Code != c.status || rec.Header().Get(HeaderError) != c.code || rec.Header().Get("Content-Type") == ContentTypeStream {
			t.Errorf("%s = %d %q %q", name, rec.Code, rec.Header().Get(HeaderError), rec.Header().Get("Content-Type"))
		}
		if strings.Contains(rec.Body.String(), "engine is down") {
			t.Errorf("%s leaked the provider error: %s", name, rec.Body)
		}
	}
}

func TestExecErrorAfterOutputIsAFrame(t *testing.T) {
	mux := serveWith(t, fake{Driver: stub.New(nil), exec: func(_ context.Context, _ string, _ runtime.Command, out func(runtime.Output) error) (runtime.Exit, error) {
		if err := out(runtime.Output{Stream: "stdout", Data: []byte("partial")}); err != nil {
			return runtime.Exit{}, err
		}
		return runtime.Exit{}, fmt.Errorf("docker: command timed out: %w", context.DeadlineExceeded)
	}})
	rec := post(mux, PathExec, `{"id":"box-1","args":["sleep","30"],"timeout_ms":1000}`)
	fr := frames(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || len(fr) != 2 {
		t.Fatalf("exec = %d %s", rec.Code, rec.Body)
	}
	if fr[0].Type != "output" || string(fr[0].Data) != "partial" {
		t.Errorf("frame 0 = %+v", fr[0])
	}
	if fr[1].Type != "error" || fr[1].Error != CodeTimeout || fr[1].Code != nil {
		t.Errorf("frame 1 = %+v", fr[1])
	}
}

func TestExecRefusesBadBodies(t *testing.T) {
	called := false
	mux := serveWith(t, fake{Driver: stub.New(nil), exec: func(context.Context, string, runtime.Command, func(runtime.Output) error) (runtime.Exit, error) {
		called = true
		return runtime.Exit{}, nil
	}})
	for _, body := range []string{
		`{`,
		`{"id":"box-1","args":["true"],"timeout_ms":-1}`,
		`{"id":"box-1","args":["true"],"timeout_ms":9223372036854775807}`,
		`{"id":"box-1","args":"true"}`,
	} {
		rec := post(mux, PathExec, body)
		if rec.Code != http.StatusBadRequest || rec.Header().Get(HeaderError) != CodeSpec {
			t.Errorf("%s = %d %q", body, rec.Code, rec.Header().Get(HeaderError))
		}
	}
	if called {
		t.Fatal("a refused body reached the provider")
	}
}

func TestExecRefusesUnknownStream(t *testing.T) {
	var outErr error
	mux := serveWith(t, fake{Driver: stub.New(nil), exec: func(_ context.Context, _ string, _ runtime.Command, out func(runtime.Output) error) (runtime.Exit, error) {
		outErr = out(runtime.Output{Stream: "stdin", Data: []byte("x")})
		return runtime.Exit{}, outErr
	}})
	rec := post(mux, PathExec, `{"id":"box-1","args":["true"]}`)
	if outErr == nil || rec.Code != http.StatusInternalServerError {
		t.Fatalf("exec = %d, out err %v", rec.Code, outErr)
	}
}

// TestExecStreamsPastServerTimeouts runs Exec on a real server whose read
// and write timeouts are shorter than the command. The first frame must
// reach the client while the command still runs, and the request context
// must outlive both timeouts.
func TestExecStreamsPastServerTimeouts(t *testing.T) {
	release := make(chan struct{})
	var cancelled atomic.Bool
	p := fake{Driver: stub.New(nil), exec: func(ctx context.Context, _ string, _ runtime.Command, out func(runtime.Output) error) (runtime.Exit, error) {
		if err := out(runtime.Output{Stream: "stdout", Data: []byte("first\n")}); err != nil {
			return runtime.Exit{}, err
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			return runtime.Exit{}, errors.New("the first frame never reached the client")
		}
		select {
		case <-ctx.Done():
			cancelled.Store(true)
			return runtime.Exit{}, ctx.Err()
		case <-time.After(600 * time.Millisecond):
		}
		if err := out(runtime.Output{Stream: "stdout", Data: []byte("second\n")}); err != nil {
			return runtime.Exit{}, err
		}
		return runtime.Exit{Code: 0}, nil
	}}
	mux := http.NewServeMux()
	Handler{Provider: p}.Mount(mux)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ReadTimeout = 200 * time.Millisecond
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+PathExec, "application/json", strings.NewReader(`{"id":"box-1","args":["sh"],"timeout_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	r := bufio.NewReader(res.Body)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var first frame
	if err := json.Unmarshal(line, &first); err != nil || string(first.Data) != "first\n" {
		t.Fatalf("first frame = %s %v", line, err)
	}
	close(release)
	var rest bytes.Buffer
	if _, err := rest.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	fr := frames(t, rest.Bytes())
	if cancelled.Load() {
		t.Fatal("the server timeouts cancelled the command")
	}
	if len(fr) != 2 || string(fr[0].Data) != "second\n" || fr[1].Type != "exit" {
		t.Fatalf("rest = %s", rest.Bytes())
	}
}

// TestExecStopsWhenClientLeaves checks that closing the stream cancels the
// command's context.
func TestExecStopsWhenClientLeaves(t *testing.T) {
	stopped := make(chan struct{})
	p := fake{Driver: stub.New(nil), exec: func(ctx context.Context, _ string, _ runtime.Command, out func(runtime.Output) error) (runtime.Exit, error) {
		if err := out(runtime.Output{Stream: "stdout", Data: []byte("tick\n")}); err != nil {
			return runtime.Exit{}, err
		}
		<-ctx.Done()
		close(stopped)
		return runtime.Exit{}, ctx.Err()
	}}
	srv := httptest.NewServer(serveWith(t, p))
	t.Cleanup(srv.Close)
	res, err := http.Post(srv.URL+PathExec, "application/json", strings.NewReader(`{"id":"box-1","args":["sh"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(res.Body).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the command kept running after the client left")
	}
}

func TestReadFile(t *testing.T) {
	files := map[string][]byte{"a.txt": []byte("hello\x00world"), "empty": nil}
	mux := serveWith(t, fake{Driver: stub.New(nil), read: func(_ context.Context, id, path string) ([]byte, error) {
		if id != "box-1" {
			return nil, runtime.ErrNotFound
		}
		switch path {
		case "dir":
			return nil, runtime.ErrNotFile
		case "big":
			return nil, runtime.ErrTooLarge
		case "":
			return nil, runtime.ErrSpec
		}
		data, ok := files[path]
		if !ok {
			return nil, fmt.Errorf("docker: /scratch/%s: %w", path, fs.ErrNotExist)
		}
		return data, nil
	}})

	rec := post(mux, PathReadFile, `{"id":"box-1","path":"a.txt"}`)
	var got struct {
		Data *[]byte `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Data == nil || string(*got.Data) != "hello\x00world" {
		t.Fatalf("read = %d %s %v", rec.Code, rec.Body, err)
	}
	rec = post(mux, PathReadFile, `{"id":"box-1","path":"empty"}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"data":""}` {
		t.Fatalf("empty read = %d %s", rec.Code, rec.Body)
	}

	for body, want := range map[string]struct {
		status int
		code   string
	}{
		`{"id":"box-1","path":"none"}`:  {http.StatusNotFound, CodeNoFile},
		`{"id":"box-2","path":"a.txt"}`: {http.StatusNotFound, CodeNotFound},
		`{"id":"box-1","path":"dir"}`:   {http.StatusUnprocessableEntity, CodeNotFile},
		`{"id":"box-1","path":"big"}`:   {http.StatusRequestEntityTooLarge, CodeTooLarge},
		`{"id":"box-1","path":""}`:      {http.StatusBadRequest, CodeSpec},
		`{`:                             {http.StatusBadRequest, CodeSpec},
	} {
		rec := post(mux, PathReadFile, body)
		if rec.Code != want.status || rec.Header().Get(HeaderError) != want.code {
			t.Errorf("%s = %d %q, want %d %q", body, rec.Code, rec.Header().Get(HeaderError), want.status, want.code)
		}
		if strings.Contains(rec.Body.String(), "/scratch") {
			t.Errorf("%s leaked the provider error: %s", body, rec.Body)
		}
	}
}

func TestWriteFile(t *testing.T) {
	var wrote []byte
	calls := 0
	mux := serveWith(t, fake{Driver: stub.New(nil), write: func(_ context.Context, id, path string, data []byte) error {
		calls++
		if id != "box-1" {
			return runtime.ErrNotFound
		}
		if path == "/missing/x" {
			return fmt.Errorf("docker: /missing/: %w", fs.ErrNotExist)
		}
		wrote = data
		return nil
	}})

	payload := []byte("binary\x00\xff")
	rec := post(mux, PathWriteFile, `{"id":"box-1","path":"a.bin","data":"`+base64.StdEncoding.EncodeToString(payload)+`"}`)
	if rec.Code != http.StatusOK || !bytes.Equal(wrote, payload) {
		t.Fatalf("write = %d %s, wrote %q", rec.Code, rec.Body, wrote)
	}
	rec = post(mux, PathWriteFile, `{"id":"box-1","path":"/missing/x","data":""}`)
	if rec.Code != http.StatusNotFound || rec.Header().Get(HeaderError) != CodeNoFile {
		t.Fatalf("missing parent = %d %q", rec.Code, rec.Header().Get(HeaderError))
	}

	calls = 0
	over := base64.StdEncoding.EncodeToString(make([]byte, runtime.MaxFile+1))
	rec = post(mux, PathWriteFile, `{"id":"box-1","path":"big","data":"`+over+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge || rec.Header().Get(HeaderError) != CodeTooLarge {
		t.Fatalf("over MaxFile = %d %q", rec.Code, rec.Header().Get(HeaderError))
	}
	huge := `{"id":"box-1","path":"big","data":"` + strings.Repeat("A", int(fileLimit)) + `"}`
	rec = post(mux, PathWriteFile, huge)
	if rec.Code != http.StatusRequestEntityTooLarge || rec.Header().Get(HeaderError) != CodeTooLarge {
		t.Fatalf("over the body limit = %d %q", rec.Code, rec.Header().Get(HeaderError))
	}
	if calls != 0 {
		t.Fatalf("an oversized write reached the provider %d times", calls)
	}
}

func TestUnbuiltRoutesAre501(t *testing.T) {
	mux := serve(t)
	for path, name := range map[string]string{PathPTY: "PTY", PathExpose: "Expose"} {
		rec := post(mux, path, `{}`)
		if rec.Code != http.StatusNotImplemented || rec.Header().Get(HeaderError) != CodeUnsupported ||
			!strings.Contains(rec.Body.String(), name+" is not built yet") {
			t.Errorf("%s = %d %q", path, rec.Code, rec.Body)
		}
	}
}

func TestExecBudget(t *testing.T) {
	limit := runtime.ResourceShape().Exec
	for timeout, want := range map[time.Duration]time.Duration{
		0:                 limit + execGrace,
		time.Second:       time.Second + execGrace,
		limit:             limit + execGrace,
		limit + time.Hour: limit + execGrace,
	} {
		if got := execBudget(timeout); got != want {
			t.Errorf("execBudget(%v) = %v, want %v", timeout, got, want)
		}
	}
}
