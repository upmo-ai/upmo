package inject

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/upmo-ai/upmo/internal/runtime"
)

type fakeResolver map[string]string

func (f fakeResolver) Resolve(_ context.Context, ref string) (string, error) {
	v, ok := f[ref]
	if !ok {
		return "", ErrNoBackend
	}
	return v, nil
}

var creds = []runtime.Credential{
	{Sentinel: "upmo_sentinel_gh", Ref: "env:GITHUB_TOKEN", Host: "github.com"},
	{Sentinel: "upmo_sentinel_npm", Ref: "env:NPM_TOKEN", Host: "registry.npmjs.org"},
}

func TestForReplacesOnlyTowardItsHost(t *testing.T) {
	resolved := fakeResolver{"env:GITHUB_TOKEN": "real-gh", "env:NPM_TOKEN": "real-npm"}
	rw, err := Injector{Resolver: resolved}.For(context.Background(), "GitHub.com.", creds)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"Authorization": {"Bearer upmo_sentinel_gh"}, "X-Other": {"upmo_sentinel_npm"}}
	out := rw.Apply(header)
	if out.Get("Authorization") != "Bearer real-gh" {
		t.Errorf("authorization = %q", out.Get("Authorization"))
	}
	if out.Get("X-Other") != "upmo_sentinel_npm" {
		t.Errorf("a credential for another host was injected: %q", out.Get("X-Other"))
	}
	if header.Get("Authorization") != "Bearer upmo_sentinel_gh" {
		t.Error("Apply changed its input")
	}
}

func TestForResolvesNothingForAnotherHost(t *testing.T) {
	rw, err := Injector{Resolver: Refuse{}}.For(context.Background(), "proxy.golang.org", creds)
	if err != nil {
		t.Fatal(err)
	}
	if !rw.Empty() {
		t.Fatal("rewrite for a host with no credential")
	}
}

func TestNoCredentialIsNotAnError(t *testing.T) {
	for name, inj := range map[string]Injector{"refuse": {Resolver: Refuse{}}, "nil resolver": {}} {
		for _, list := range [][]runtime.Credential{nil, {}, creds} {
			rw, err := inj.For(context.Background(), "proxy.golang.org", list)
			if err != nil || !rw.Empty() {
				t.Fatalf("%s with %d credentials = %v empty=%t", name, len(list), err, rw.Empty())
			}
		}
	}
}

func TestRefuseResolvesNothing(t *testing.T) {
	if _, err := (Injector{Resolver: Refuse{}}).For(context.Background(), "github.com", creds); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("err = %v", err)
	}
	if _, err := (Injector{}).For(context.Background(), "github.com", creds); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("nil resolver err = %v", err)
	}
	if _, err := (Injector{Resolver: fakeResolver{"env:GITHUB_TOKEN": ""}}).For(context.Background(), "github.com", creds); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("empty value err = %v", err)
	}
}

func TestEnvResolvesFromTheProcess(t *testing.T) {
	t.Setenv("UPMO_INJECT_TEST_TOKEN", "real-from-env")
	rw, err := Injector{Resolver: Env{}}.For(context.Background(), "github.com", []runtime.Credential{
		{Sentinel: "upmo_sentinel_gh", Ref: "env:UPMO_INJECT_TEST_TOKEN", Host: "github.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := rw.Apply(http.Header{"Authorization": {"Bearer upmo_sentinel_gh"}})
	if out.Get("Authorization") != "Bearer real-from-env" {
		t.Fatalf("authorization = %q", out.Get("Authorization"))
	}
}

func TestEnvRefusesBadRefs(t *testing.T) {
	t.Setenv("UPMO_INJECT_TEST_TOKEN", "real")
	cases := []string{
		"vault:secret",
		"env:",
		"env:1BAD",
		"env:HAS-DASH",
		"env:MISSING_NAME",
		"ENV:UPMO_INJECT_TEST_TOKEN",
	}
	for _, ref := range cases {
		if _, err := (Env{}).Resolve(context.Background(), ref); !errors.Is(err, ErrNoBackend) {
			t.Fatalf("Resolve(%q) = %v, want ErrNoBackend", ref, err)
		}
	}
	t.Setenv("UPMO_INJECT_EMPTY", "")
	if _, err := (Env{}).Resolve(context.Background(), "env:UPMO_INJECT_EMPTY"); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("empty env = %v", err)
	}
}
