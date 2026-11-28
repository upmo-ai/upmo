package app

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const task = "018f3b2c-7a10-7c4a-8e11-111111111111"

func validSpec() Spec {
	return Spec{
		Task:  task,
		Image: "upmo/sandbox:base",
		Tier:  "container",
		TTL:   time.Hour,
		Allow: []string{"github.com", "Proxy.Golang.org."},
		Credentials: []Credential{
			{Sentinel: "upmo_sentinel_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Ref: "github_token", Host: "github.com", Methods: []string{"GET"}, Paths: []string{"/user"}},
		},
	}
}

func TestCheckSpecNormalizes(t *testing.T) {
	spec := validSpec()
	spec.Tier = " GVisor "
	spec.Allow = append(spec.Allow, "github.com")
	got, err := CheckSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != "gvisor" {
		t.Errorf("tier = %q", got.Tier)
	}
	if !slices.Equal(got.Allow, []string{"github.com", "proxy.golang.org"}) {
		t.Errorf("allow = %v", got.Allow)
	}
	if len(got.Credentials) != 1 || got.Credentials[0].Host != "github.com" {
		t.Errorf("credentials = %+v", got.Credentials)
	}
}

func TestCheckSpecRejectsUnboundCredential(t *testing.T) {
	mutations := []func(*Credential){
		func(c *Credential) { c.Sentinel = "ghp_fake" },
		func(c *Credential) { c.Sentinel = "upmo_sentinel_ab" },
		func(c *Credential) {
			c.Sentinel = "upmo_sentinel_0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
		},
		func(c *Credential) {
			c.Sentinel = "upmo_sentinel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		func(c *Credential) { c.Ref = "env:GITHUB_TOKEN" },
		func(c *Credential) { c.Ref = SentinelPrefix + strings.Repeat("a", 64) },
		func(c *Credential) { c.Methods = nil },
		func(c *Credential) { c.Methods = []string{"CONNECT"} },
		func(c *Credential) { c.Methods = []string{" get"} },
		func(c *Credential) { c.Paths = nil },
		func(c *Credential) { c.Paths = []string{"/"} },
		func(c *Credential) { c.Paths = []string{"/user/../repos"} },
		func(c *Credential) { c.Paths = []string{"/user%2f"} },
	}
	for n, mutate := range mutations {
		t.Run(string(rune('a'+n)), func(t *testing.T) {
			s := validSpec()
			mutate(&s.Credentials[0])
			if _, err := CheckSpec(s); !errors.Is(err, ErrSpec) {
				t.Fatalf("err = %v, want ErrSpec", err)
			}
		})
	}
	s := validSpec()
	s.Credentials = append(s.Credentials, s.Credentials[0])
	s.Credentials[1].Sentinel = s.Credentials[0].Sentinel + "a"
	if _, err := CheckSpec(s); !errors.Is(err, ErrSpec) {
		t.Fatalf("overlapping sentinels err = %v", err)
	}
	s = validSpec()
	s.ProxySecret = []byte(strings.Repeat("p", 32))
	got, err := CheckSpec(s)
	if err != nil || len(got.ProxySecret) != 32 {
		t.Fatalf("32-byte proxy secret: len=%d err=%v", len(got.ProxySecret), err)
	}
	got.ProxySecret[0] = 'x'
	if s.ProxySecret[0] != 'p' {
		t.Fatal("CheckSpec did not clone proxy secret")
	}
	s.ProxySecret = make([]byte, 31)
	if _, err := CheckSpec(s); !errors.Is(err, ErrSpec) {
		t.Fatalf("31-byte proxy secret err = %v", err)
	}
}

func TestNewSentinel(t *testing.T) {
	a, err := NewSentinel()
	if err != nil || !sentinelPattern(a) || !strings.HasPrefix(a, SentinelPrefix) {
		t.Fatalf("NewSentinel() = %q, %v", a, err)
	}
	b, err := NewSentinel()
	if err != nil || a == b {
		t.Fatalf("NewSentinel repeat = %q, %v", b, err)
	}
}

func TestCheckSpecRefuses(t *testing.T) {
	cases := map[string]func(*Spec){
		"blank task":          func(s *Spec) { s.Task = "" },
		"nil task":            func(s *Spec) { s.Task = "00000000-0000-0000-0000-000000000000" },
		"blank image":         func(s *Spec) { s.Image = " " },
		"image with space":    func(s *Spec) { s.Image = "a b" },
		"unknown tier":        func(s *Spec) { s.Tier = "microvm" },
		"zero ttl":            func(s *Spec) { s.TTL = 0 },
		"address allow":       func(s *Spec) { s.Allow = []string{"10.0.0.1"} },
		"host allow":          func(s *Spec) { s.Allow = []string{"localhost"} },
		"allow with port":     func(s *Spec) { s.Allow = []string{"github.com:443"} },
		"credential off list": func(s *Spec) { s.Credentials[0].Host = "example.com" },
		"blank sentinel":      func(s *Spec) { s.Credentials[0].Sentinel = "" },
		"blank ref":           func(s *Spec) { s.Credentials[0].Ref = "" },
		"sentinel is ref":     func(s *Spec) { s.Credentials[0].Ref = "upmo_sentinel_" + strings.Repeat("a", 64) },
		"duplicate sentinel":  func(s *Spec) { s.Credentials = append(s.Credentials, s.Credentials[0]) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validSpec()
			mutate(&spec)
			if _, err := CheckSpec(spec); !errors.Is(err, ErrSpec) {
				t.Fatalf("err = %v, want ErrSpec", err)
			}
		})
	}
}
