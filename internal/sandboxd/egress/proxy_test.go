package egress

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
)

type book map[string][]string

func (b book) Egress(id string) ([]string, []runtime.Credential, bool) {
	allow, ok := b[id]
	return allow, []runtime.Credential{{Sentinel: "upmo_sentinel_gh", Ref: "env:GITHUB_TOKEN", Host: "github.com"}}, ok
}

type injector struct {
	hosts []string
	err   error
}

func (i *injector) For(_ context.Context, host string, _ []runtime.Credential) (inject.Rewrite, error) {
	i.hosts = append(i.hosts, host)
	return inject.Rewrite{}, i.err
}

func connect(p Proxy, target, sandbox string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodConnect, target, nil)
	if sandbox != "" {
		r.Header.Set("Proxy-Authorization", "Basic "+basic(sandbox))
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, r)
	return rec
}

func basic(user string) string {
	r := &http.Request{Header: http.Header{}}
	r.SetBasicAuth(user, "")
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Basic ")
}

func proxy(inj *injector) (Proxy, *bytes.Buffer) {
	var logs bytes.Buffer
	return Proxy{
		Book:   book{"box-1": {"github.com"}},
		Inject: inj,
		Log:    slog.New(slog.NewJSONHandler(&logs, nil)),
	}, &logs
}

func TestRefusesUnlistedAndLogs(t *testing.T) {
	inj := &injector{}
	for target, reason := range map[string]string{
		"example.com:443":      "allowlist",
		"169.254.169.254:80":   "address",
		"metadata.internal:80": "host",
	} {
		p, logs := proxy(inj)
		rec := connect(p, target, "box-1")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d", target, rec.Code)
		}
		if !strings.Contains(logs.String(), `"verdict":"refuse"`) || !strings.Contains(logs.String(), `"reason":"`+reason+`"`) {
			t.Errorf("%s log = %s", target, logs)
		}
	}
	if len(inj.hosts) != 0 {
		t.Fatalf("credentials resolved for a refused host: %v", inj.hosts)
	}
}

func TestListedHostResolvesCredentialsThenStops(t *testing.T) {
	inj := &injector{}
	p, logs := proxy(inj)
	rec := connect(p, "github.com:443", "box-1")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("listed = %d %s", rec.Code, rec.Body)
	}
	if len(inj.hosts) != 1 || inj.hosts[0] != "github.com" {
		t.Fatalf("inject hosts = %v", inj.hosts)
	}
	if !strings.Contains(logs.String(), `"verdict":"allow"`) || !strings.Contains(logs.String(), `"port":"443"`) {
		t.Fatalf("log = %s", logs)
	}

	inj.err = inject.ErrNoBackend
	if rec := connect(p, "github.com:443", "box-1"); rec.Code != http.StatusBadGateway {
		t.Fatalf("no backend = %d", rec.Code)
	}
}

func TestRefusesUnknownSandboxAndOtherMethods(t *testing.T) {
	p, _ := proxy(&injector{})
	if rec := connect(p, "github.com:443", ""); rec.Code != http.StatusProxyAuthRequired {
		t.Errorf("anonymous = %d", rec.Code)
	}
	if rec := connect(p, "github.com:443", "box-2"); rec.Code != http.StatusProxyAuthRequired {
		t.Errorf("unknown = %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://github.com/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", rec.Code)
	}
}
