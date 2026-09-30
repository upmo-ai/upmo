package app

import (
	"errors"
	"slices"
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
			{Sentinel: "upmo_sentinel_gh", Ref: "env:GITHUB_TOKEN", Host: "github.com"},
		},
	}
}

func TestCheckSpecNormalises(t *testing.T) {
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
		"sentinel is ref":     func(s *Spec) { s.Credentials[0].Ref = s.Credentials[0].Sentinel },
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
