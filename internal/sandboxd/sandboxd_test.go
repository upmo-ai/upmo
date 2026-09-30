package sandboxd

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime"
)

const pathCreate = "/upmo.sandboxd.v1.Runtime/Create"

func serviceKey(b byte) string {
	return svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat(string(b), 32)))
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

func TestRoutesRefuseWithoutServiceBearer(t *testing.T) {
	key := serviceKey('k')
	srv := start(t, key, "")
	master, err := svcsig.ParseKey(key)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := svcsig.Derive(master, svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	gatewayBearer, err := svcsig.Sign(gateway, svcsig.AudienceGateway, http.MethodPost, pathCreate, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, auth := range map[string]string{ //nolint:gosec // fixed test bearers, not credentials
		"no bearer":        "",
		"personal token":   "Bearer upmo_pat_0123456789abcdef",
		"gateway audience": "Bearer " + gatewayBearer,
		"not a bearer":     "Basic dXNlcjpwYXNz",
	} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+pathCreate, strings.NewReader(`{}`))
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
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, res.StatusCode)
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
}

func TestProxyRefusesUnlistedHost(t *testing.T) {
	driver := Stub()
	box, err := driver.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	p := Proxy(driver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for target, code := range map[string]int{
		"example.com:443": http.StatusForbidden,
		// Listed, but the stub resolves no credential and builds no tunnel.
		"github.com:443": http.StatusNotImplemented,
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
