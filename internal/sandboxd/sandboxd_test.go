package sandboxd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime"
	httpapi "github.com/upmo-ai/upmo/internal/sandboxd/adapters/http"
	"github.com/upmo-ai/upmo/internal/testutil/tasksandbox"
)

const (
	pathCreate    = httpapi.PathCreate
	pathExec      = httpapi.PathExec
	pathReadFile  = httpapi.PathReadFile
	pathWriteFile = httpapi.PathWriteFile
)

func serviceKey(b byte) string {
	return svcsig.KeyPrefix + "sandboxd_" + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat(string(b), 32)))
}

func spec() runtime.Spec {
	return runtime.Spec{
		Task:  "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image: "upmo/sandbox:base",
		Tier:  runtime.TierContainer,
		TTL:   10 * time.Minute,
		Allow: []string{"github.com"},
	}
}

func start(t *testing.T, current, previous string) *httptest.Server {
	t.Helper()
	h, err := Routes(current, previous, Stub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestBrainClientCreatesAndDestroys runs brain's runtime client against the
// real sandboxd routes, signed with the sandboxd audience.
func TestBrainClientCreatesAndDestroys(t *testing.T) {
	key := serviceKey('k')
	srv := start(t, key, "")
	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	box, err := provider.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if box.ID == "" || box.Shape != runtime.ResourceShape() {
		t.Fatalf("box = %+v", box)
	}
	if err := provider.Pause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if err := provider.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if err := provider.Destroy(ctx, box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("second destroy err = %v", err)
	}
	if _, err := provider.Exec(ctx, box.ID, runtime.Command{Args: []string{"true"}}, nil); !errors.Is(err, runtime.ErrUnsupported) {
		t.Fatalf("exec err = %v", err)
	}
	gvisor := spec()
	gvisor.Tier = runtime.TierGVisor
	if _, err := provider.Create(ctx, gvisor); !errors.Is(err, runtime.ErrIsolation) {
		t.Fatalf("gvisor on the stub err = %v", err)
	}
}

// shell is the stub driver with an in-memory Exec and files, so brain's
// client runs Exec, ReadFile and WriteFile against the real routes.
type shell struct {
	Driver
	mu    sync.Mutex
	files map[string][]byte
	ran   []string
}

func newShell() *shell { return &shell{Driver: Stub(), files: map[string][]byte{}} }

func (s *shell) live(id string) bool {
	_, _, ok := s.Egress(id)
	return ok
}

// Exec knows echo, which writes its arguments to stdout, fail, which writes
// to stderr and exits 2, sleep, which runs until its timeout, and sh, env
// and git, which succeed and are recorded.
func (s *shell) Exec(ctx context.Context, id string, cmd runtime.Command, out func(runtime.Output) error) (runtime.Exit, error) {
	if !s.live(id) {
		return runtime.Exit{}, runtime.ErrNotFound
	}
	if len(cmd.Args) == 0 {
		return runtime.Exit{}, runtime.ErrSpec
	}
	switch cmd.Args[0] {
	case "echo":
		return runtime.Exit{}, out(runtime.Output{Stream: "stdout", Data: []byte(strings.Join(cmd.Args[1:], " ") + "\n")})
	case "fail":
		if err := out(runtime.Output{Stream: "stderr", Data: []byte("failed\n")}); err != nil {
			return runtime.Exit{}, err
		}
		return runtime.Exit{Code: 2}, nil
	case "sleep":
		if err := out(runtime.Output{Stream: "stdout", Data: []byte("started\n")}); err != nil {
			return runtime.Exit{}, err
		}
		select {
		case <-ctx.Done():
			return runtime.Exit{}, ctx.Err()
		case <-time.After(cmd.Timeout):
			return runtime.Exit{}, fmt.Errorf("shell: command timed out: %w", context.DeadlineExceeded)
		}
	case "sh", "env", "git":
		// The provisioning commands, which pass here.
		s.mu.Lock()
		s.ran = append(s.ran, strings.Join(cmd.Args, " "))
		s.mu.Unlock()
		return runtime.Exit{}, nil
	default:
		return runtime.Exit{Code: 127}, nil
	}
}

func (s *shell) ReadFile(_ context.Context, id, path string) ([]byte, error) {
	if !s.live(id) {
		return nil, runtime.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[id+"/"+path]
	if !ok {
		return nil, fmt.Errorf("shell: %s: %w", path, fs.ErrNotExist)
	}
	return data, nil
}

func (s *shell) WriteFile(_ context.Context, id, path string, data []byte) error {
	if !s.live(id) {
		return runtime.ErrNotFound
	}
	if len(data) > runtime.MaxFile {
		return runtime.ErrTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[id+"/"+path] = append([]byte(nil), data...)
	return nil
}

// TestBrainClientExecAndFiles runs brain's runtime client against the real
// sandboxd routes and HMAC: Exec streams output and its exit code, files
// round-trip as bytes, and each refusal comes back as the port's error.
func TestBrainClientExecAndFiles(t *testing.T) {
	key := serviceKey('k')
	h, err := Routes(key, "", newShell(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	box, err := provider.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, int, error) {
		var got strings.Builder
		exit, err := provider.Exec(ctx, box.ID, runtime.Command{Args: args, Timeout: 200 * time.Millisecond}, func(o runtime.Output) error {
			got.WriteString(o.Stream + ":" + string(o.Data))
			return nil
		})
		return got.String(), exit.Code, err
	}
	if out, code, err := run("echo", "hello", "sandbox"); err != nil || code != 0 || out != "stdout:hello sandbox\n" {
		t.Fatalf("echo = %q %d %v", out, code, err)
	}
	if out, code, err := run("fail"); err != nil || code != 2 || out != "stderr:failed\n" {
		t.Fatalf("fail = %q %d %v", out, code, err)
	}
	if out, _, err := run("sleep"); !errors.Is(err, context.DeadlineExceeded) || out != "stdout:started\n" {
		t.Fatalf("sleep = %q %v", out, err)
	}
	if _, err := provider.Exec(ctx, "018f3b2c-7a10-7c4a-8e11-999999999999", runtime.Command{Args: []string{"echo"}}, nil); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("exec on an unknown sandbox err = %v", err)
	}

	data := []byte("binary\x00\xff\n")
	if err := provider.WriteFile(ctx, box.ID, "notes/a.bin", data); err != nil {
		t.Fatal(err)
	}
	if got, err := provider.ReadFile(ctx, box.ID, "notes/a.bin"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read = %q %v", got, err)
	}
	if _, err := provider.ReadFile(ctx, box.ID, "none"); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("missing file err = %v", err)
	}
	if err := provider.WriteFile(ctx, "018f3b2c-7a10-7c4a-8e11-999999999999", "a", nil); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("write to an unknown sandbox err = %v", err)
	}

	if err := provider.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ReadFile(ctx, box.ID, "notes/a.bin"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("read after destroy err = %v", err)
	}
}

// TestBrainTaskSandboxesProvisionAndRelease runs the task sandboxes brain uses on the
// Preparing path against the real sandboxd routes: Provision creates the
// sandbox and runs every phase in the background through Exec and the file
// routes, and Release destroys it so sandboxd no longer holds it.
func TestBrainTaskSandboxesProvisionAndRelease(t *testing.T) {
	key := serviceKey('k')
	sh := newShell()
	h, err := Routes(key, "", sh, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	book := &tasksandbox.Memory{}
	boxes := &runtime.TaskSandboxes{Provider: provider, Store: book}
	ctx := context.Background()
	task := spec().Task
	outcome := make(chan error, 1)
	needed, err := boxes.Provision(ctx, task, "org/name", []runtime.Class{runtime.ClassShell}, func(_ context.Context, err error) error {
		outcome <- err
		return nil
	})
	if err != nil || !needed {
		t.Fatalf("provision = %t %v", needed, err)
	}
	if err := <-outcome; err != nil {
		t.Fatalf("provisioning = %v", err)
	}
	if rec, ok := boxes.Progress(task); !ok || rec.Phase != runtime.PhaseReady {
		t.Fatalf("progress = %+v %t", rec, ok)
	}
	sh.mu.Lock()
	ran := strings.Join(sh.ran, "\n")
	sh.mu.Unlock()
	if !strings.Contains(ran, "git clone --depth=1 --quiet -- https://github.com/org/name.git /scratch/repo") || !strings.Contains(ran, "rev-parse") {
		t.Fatalf("ran = %s", ran)
	}
	id, ok, err := boxes.Held(ctx, task)
	if err != nil || !ok {
		t.Fatalf("no sandbox after provision: %v", err)
	}
	if err := boxes.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := provider.Destroy(ctx, id); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("sandbox still held after release: %v", err)
	}
	if book.Len() != 0 {
		t.Fatalf("rows after release = %d", book.Len())
	}
}

// TestBrainSeatsProvisionOnTheStubFails shows the stub, which runs no
// command, cannot provision: Provision returns once the sandbox is held, then
// the clone phase fails with ErrUnsupported in the background and the binding
// is still held for done to release.
func TestBrainSeatsProvisionOnTheStubFails(t *testing.T) {
	key := serviceKey('k')
	srv := start(t, key, "")
	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	boxes := &runtime.TaskSandboxes{Provider: provider, Store: &tasksandbox.Memory{}}
	ctx := context.Background()
	task := spec().Task
	outcome := make(chan error, 1)
	if _, err := boxes.Provision(ctx, task, "", []runtime.Class{runtime.ClassShell}, func(_ context.Context, err error) error {
		outcome <- err
		return nil
	}); err != nil {
		t.Fatalf("provision = %v", err)
	}
	err = <-outcome
	var phase *runtime.PhaseError
	if !errors.As(err, &phase) || phase.Phase != runtime.PhaseClone || !errors.Is(err, runtime.ErrUnsupported) {
		t.Fatalf("provisioning = %v", err)
	}
	if rec, ok := boxes.Progress(task); !ok || rec.Phase != runtime.PhaseFailed {
		t.Fatalf("progress = %+v %t", rec, ok)
	}
	if err := boxes.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
}

func TestRoutesRefuseWithoutServiceBearer(t *testing.T) {
	key := serviceKey('k')
	srv := start(t, key, "")
	gateway, err := svcsig.ParseAudienceKey(svcsig.KeyPrefix+"gateway_"+base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	gatewayBearer, err := svcsig.SignRequest(gateway, svcsig.AudienceGateway, http.MethodPost, pathCreate, "", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// A bearer signed for Create is bound to that path, so it opens no other route.
	createBearer, err := svcsig.SignRequest(derivedKey(t, key), svcsig.AudienceSandboxd, http.MethodPost, pathCreate, "", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The same MAC under the retired v1 prefix is refused on every route.
	v1Bearer := "upmo_s2s_v1." + strings.TrimPrefix(createBearer, svcsig.BearerPrefixV2)
	for name, auth := range map[string]string{ //nolint:gosec // fixed test bearers, not credentials
		"no bearer":        "",
		"personal token":   "Bearer upmo_pat_0123456789abcdef",
		"gateway audience": "Bearer " + gatewayBearer,
		"v1 bearer":        "Bearer " + v1Bearer,
		"not a bearer":     "Basic dXNlcjpwYXNz",
	} {
		for _, path := range []string{pathCreate, pathExec, pathReadFile, pathWriteFile} {
			if code := postWith(t, srv.URL+path, auth); code != http.StatusUnauthorized {
				t.Errorf("%s on %s = %d, want 401", name, path, code)
			}
		}
	}
	for _, path := range []string{pathExec, pathReadFile, pathWriteFile} {
		if code := postWith(t, srv.URL+path, "Bearer "+createBearer); code != http.StatusUnauthorized {
			t.Errorf("a Create bearer on %s = %d, want 401", path, code)
		}
	}

	for _, path := range []string{"/healthz", "/readyz"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s = %d", path, res.StatusCode)
		}
	}
}

func derivedKey(t *testing.T, key string) []byte {
	t.Helper()
	derived, err := svcsig.ParseAudienceKey(key, svcsig.AudienceSandboxd)
	if err != nil {
		t.Fatal(err)
	}
	return derived
}

// postWith posts {} to url with auth as the Authorization header, when set,
// and returns the status.
func postWith(t *testing.T, url, auth string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func TestRoutesAcceptPreviousKey(t *testing.T) {
	current, previous := serviceKey('k'), serviceKey('p')
	srv := start(t, current, previous)
	for _, key := range []string{current, previous} {
		provider, err := runtime.Open(srv.URL, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Create(context.Background(), spec()); err != nil {
			t.Fatalf("key %c: %v", key[len(key)-1], err)
		}
	}
	other, err := runtime.Open(srv.URL, serviceKey('x'))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Create(context.Background(), spec()); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

func TestRoutesNeedAKey(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := Routes("", "", Stub(), log); err == nil {
		t.Error("no key")
	}
	if _, err := Routes("upmo_pat_abc", "", Stub(), log); err == nil {
		t.Error("personal token as key")
	}
	if _, err := Routes(serviceKey('k'), "nope", Stub(), log); err == nil {
		t.Error("malformed previous key")
	}
	if _, err := Routes(svcsig.KeyPrefix+base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "", Stub(), log); err == nil {
		t.Error("raw service master as key")
	}
}

func TestProxyRefusesUnlistedHost(t *testing.T) {
	driver := Stub()
	s := spec()
	s.Allow = append(s.Allow, "proxy.golang.org")
	s.Credentials = []runtime.Credential{{Sentinel: "upmo_sentinel_gh", Ref: "env:GITHUB_TOKEN", Host: "github.com"}}
	box, err := driver.Create(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	p := Proxy(driver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for target, code := range map[string]int{
		"example.com:443": http.StatusForbidden,
		// Listed, but its credential has no backend to resolve it.
		"github.com:443": http.StatusBadGateway,
		// Listed with no credential: admitted as far as the tunnel, which a
		// recorder cannot be hijacked for, so nothing is dialled.
		"proxy.golang.org:443": http.StatusInternalServerError,
	} {
		r := httptest.NewRequest(http.MethodConnect, target, nil)
		u := &http.Request{Header: http.Header{}}
		u.SetBasicAuth(box.ID, "")
		r.Header.Set("Proxy-Authorization", u.Header.Get("Authorization"))
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, r)
		if rec.Code != code {
			t.Errorf("%s = %d, want %d", target, rec.Code, code)
		}
	}
}

// swept is a driver that records sweeps.
type swept struct {
	Driver
	n chan struct{}
}

func (s swept) Sweep(context.Context) (int, error) {
	s.n <- struct{}{}
	return 0, nil
}

func TestOpenPicksTheDriver(t *testing.T) {
	ctx := context.Background()
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))
	docker := swept{Driver: Stub()}
	reachable := func(context.Context) (Driver, error) { return docker, nil }
	down := func(context.Context) (Driver, error) { return nil, errors.New("no socket") }
	never := func(context.Context) (Driver, error) {
		t.Fatal("the stub choice dialled the engine")
		return nil, nil
	}

	for _, choice := range []string{"", DriverAuto, DriverDocker} {
		d, err := open(ctx, choice, Network{}, "", "", log, reachable)
		if err != nil || d != Driver(docker) {
			t.Fatalf("%q with an engine = %T %v", choice, d, err)
		}
	}

	logs.Reset()
	d, err := open(ctx, DriverAuto, Network{}, "", "", log, down)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.(swept); ok {
		t.Fatal("auto without an engine kept the docker driver")
	}
	if _, err := d.Exec(ctx, "x", runtime.Command{Args: []string{"true"}}, nil); !errors.Is(err, runtime.ErrUnsupported) {
		t.Fatalf("fallback is not the stub: %v", err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "stub") {
		t.Fatalf("fallback was not logged: %s", logs.String())
	}

	if _, err := open(ctx, DriverDocker, Network{}, "", "", log, down); err == nil {
		t.Fatal("docker without an engine fell back")
	}
	if _, err := open(ctx, DriverStub, Network{}, "", "", log, never); err != nil {
		t.Fatal(err)
	}
	if _, err := open(ctx, "nomad", Network{}, "", "", log, never); err == nil {
		t.Fatal("unknown driver accepted")
	}
}

func TestSweepRunsUntilCanceled(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := swept{Driver: Stub(), n: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Sweep(ctx, s, 10*time.Millisecond, log)
		close(done)
	}()
	for range 2 {
		select {
		case <-s.n:
		case <-time.After(2 * time.Second):
			t.Fatal("no sweep")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sweep did not stop")
	}

	// The stub holds nothing outside the process, so there is nothing to sweep.
	stubDone := make(chan struct{})
	go func() {
		Sweep(context.Background(), Stub(), time.Millisecond, log)
		close(stubDone)
	}()
	select {
	case <-stubDone:
	case <-time.After(2 * time.Second):
		t.Fatal("sweep ran for the stub")
	}
}

func TestRoutesWithMetricsTracksActiveSeatsAndHidesMetricsPath(t *testing.T) {
	key := serviceKey('m')
	reg := metrics.NewRegistry()
	h, err := Routes(key, "", Stub(), slog.New(slog.NewTextHandler(io.Discard, nil)), WithMetrics(reg))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// /metrics is never exposed on the RPC mux.
	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /metrics on RPC mux = %d, want 401 or 404", res.StatusCode)
	}

	scrape := func() string {
		rec := httptest.NewRecorder()
		reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("metrics status = %d", rec.Code)
		}
		return rec.Body.String()
	}

	if body := scrape(); !strings.Contains(body, "upmo_sandboxd_active_seats 0\n") {
		t.Fatalf("initial active seats != 0:\n%s", body)
	}

	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	box, err := provider.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	body := scrape()
	if !strings.Contains(body, "upmo_sandboxd_active_seats 1\n") {
		t.Fatalf("active seats after Create != 1:\n%s", body)
	}
	if !strings.Contains(body, `upmo_http_requests_total{service="sandboxd",route="POST `+pathCreate+`",status="200"} 1`) {
		t.Fatalf("missing sandboxd create HTTP metric:\n%s", body)
	}

	if err := provider.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if body := scrape(); !strings.Contains(body, "upmo_sandboxd_active_seats 0\n") {
		t.Fatalf("active seats after Destroy != 0:\n%s", body)
	}
}

// TestMetricsListenerNeedsTheScrapeToken serves the sandboxd registry the way
// cmd/sandboxd does and proves only the scrape token reads it.
func TestMetricsListenerNeedsTheScrapeToken(t *testing.T) {
	const token = "upmo_mtr_c2FuZGJveGQtbWV0cmljcy1zY3JhcGUtdG9rZW4tdGVzdA" // #nosec G101 -- fixed test token, not a credential
	reg := metrics.NewRegistry()
	if _, err := Routes(serviceKey('m'), "", Stub(), slog.New(slog.NewTextHandler(io.Discard, nil)), WithMetrics(reg)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(metrics.Server("127.0.0.1:0", reg, token).Handler)
	t.Cleanup(srv.Close)
	gateway, err := svcsig.ParseAudienceKey(svcsig.KeyPrefix+"gateway_"+base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("g", 32))), svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	gatewayBearer, err := svcsig.SignRequest(gateway, svcsig.AudienceGateway, http.MethodGet, "/metrics", "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scrape := func(auth string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
	for name, auth := range map[string]string{"no bearer": "", "gateway bearer": "Bearer " + gatewayBearer} {
		if code, _ := scrape(auth); code != http.StatusUnauthorized {
			t.Fatalf("%s = %d, want 401", name, code)
		}
	}
	code, body := scrape("Bearer " + token)
	if code != http.StatusOK || !strings.Contains(body, "upmo_sandboxd_active_seats 0\n") {
		t.Fatalf("scrape token = %d:\n%s", code, body)
	}
}
