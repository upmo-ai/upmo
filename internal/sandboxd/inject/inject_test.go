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
	t.Setenv("INJECT_TEST_TOKEN", "real-from-env")
	rw, err := Injector{Resolver: Env{Allow: []string{"INJECT_TEST_TOKEN"}}}.For(context.Background(), "github.com", []runtime.Credential{
		{Sentinel: "upmo_sentinel_gh", Ref: "env:INJECT_TEST_TOKEN", Host: "github.com"},
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
	t.Setenv("INJECT_TEST_TOKEN", "real")
	t.Setenv("INJECT_EMPTY", "")
	env := Env{Allow: []string{"INJECT_TEST_TOKEN", "INJECT_EMPTY", "MISSING_NAME"}}
	cases := []string{
		"vault:secret",
		"env:",
		"env:1BAD",
		"env:HAS-DASH",
		"env:MISSING_NAME",
		"ENV:INJECT_TEST_TOKEN",
		"env:INJECT_EMPTY",
	}
	for _, ref := range cases {
		if _, err := env.Resolve(context.Background(), ref); !errors.Is(err, ErrNoBackend) {
			t.Fatalf("Resolve(%q) = %v, want ErrNoBackend", ref, err)
		}
	}
}

func TestEnvRefusesUpmoNamesEvenWhenListed(t *testing.T) {
	t.Setenv("UPMO_MASTER_KEY", "upmo_master_test_value")
	t.Setenv("UPMO_SERVICE_KEY", "upmo_service_test_value")
	env := Env{Allow: []string{"UPMO_MASTER_KEY", "UPMO_SERVICE_KEY"}}
	for _, ref := range []string{"env:UPMO_MASTER_KEY", "env:UPMO_SERVICE_KEY"} {
		value, err := env.Resolve(context.Background(), ref)
		if !errors.Is(err, ErrNoBackend) || value != "" {
			t.Fatalf("Resolve(%q) = %q, %v, want ErrNoBackend", ref, value, err)
		}
		if err.Error() != "inject: no credential backend" {
			t.Fatalf("Resolve(%q) err text = %q", ref, err)
		}
	}
}

func TestEnvResolvesOnlyItsAllowList(t *testing.T) {
	t.Setenv("NOT_A_CREDENTIAL", "sk-test-unlisted")
	t.Setenv("ALLOWED_REF", "sk-test-allowed")
	env := Env{Allow: []string{"ALLOWED_REF"}}
	if value, err := env.Resolve(context.Background(), "env:NOT_A_CREDENTIAL"); !errors.Is(err, ErrNoBackend) || value != "" {
		t.Fatalf("unlisted = %q, %v, want ErrNoBackend", value, err)
	}
	value, err := env.Resolve(context.Background(), "env:ALLOWED_REF")
	if err != nil || value != "sk-test-allowed" {
		t.Fatalf("listed = %q, %v, want sk-test-allowed", value, err)
	}
	// Exact match only: no prefix or case folding.
	for _, list := range [][]string{{"ALLOWED"}, {"allowed_ref"}, {"ALLOWED_REF_X"}} {
		if _, err := (Env{Allow: list}).Resolve(context.Background(), "env:ALLOWED_REF"); !errors.Is(err, ErrNoBackend) {
			t.Fatalf("allow %v resolved ALLOWED_REF: %v", list, err)
		}
	}
	if value, err := (Env{}).Resolve(context.Background(), "env:ALLOWED_REF"); !errors.Is(err, ErrNoBackend) || value != "" {
		t.Fatalf("zero Env = %q, %v, want ErrNoBackend", value, err)
	}
}
