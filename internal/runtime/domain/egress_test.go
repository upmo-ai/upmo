package domain

import (
	"errors"
	"reflect"
	"testing"
)

var githubAllow = []string{"api.github.com"}

func TestAdmitEgressRejectsMalformedHost(t *testing.T) {
	for _, host := range []string{
		"", "   ",
		"https://api.github.com", "api.github.com/x", "user@api.github.com",
		"api.github.com:0", "api.github.com:65536", "api.github.com:0443", "api.github.com:",
		"*.github.com", "api github.com", "api.gïthub.com", "-api.github.com", "[api.github.com]",
	} {
		got, err := AdmitEgress(host, githubAllow)
		if !errors.Is(err, ErrEgress) {
			t.Errorf("AdmitEgress(%q) error = %v, want %v", host, err, ErrEgress)
		}
		if got != (Decision{}) {
			t.Errorf("AdmitEgress(%q) = %+v, want zero Decision", host, got)
		}
	}
}

func TestAdmitEgressRejectsMalformedAllowEntry(t *testing.T) {
	for _, entry := range []string{
		"", "   ", "*", "*.example.com", "127.0.0.1", "[::1]", "localhost", "brain",
		"api.github.com:443", "printer.local", "localhost.localdomain", "https://api.github.com",
	} {
		for _, host := range []string{"api.github.com", "localhost", "127.0.0.1"} {
			got, err := AdmitEgress(host, []string{"api.github.com", entry})
			if !errors.Is(err, ErrEgress) {
				t.Errorf("AdmitEgress(%q, %q) error = %v, want %v", host, entry, err, ErrEgress)
			}
			if got != (Decision{}) {
				t.Errorf("AdmitEgress(%q, %q) = %+v, want zero Decision", host, entry, got)
			}
		}
	}
}

func TestAdmitEgressDoesNotMutateAllow(t *testing.T) {
	backing := []string{" API.GitHub.com. ", "example.com", "spare", "spare"}
	want := append([]string(nil), backing...)
	allow := backing[:2]

	if _, err := AdmitEgress("api.github.com", allow); err != nil {
		t.Fatalf("AdmitEgress() error = %v", err)
	}
	if !reflect.DeepEqual(backing, want) {
		t.Fatalf("AdmitEgress() allow backing = %q, want %q", backing, want)
	}
}

func TestAdmitEgressEmptyAllowRefusesPublicName(t *testing.T) {
	got, err := AdmitEgress("api.github.com", nil)
	if err != nil {
		t.Fatalf("AdmitEgress() error = %v", err)
	}
	want := Decision{Verdict: VerdictRefuse, Host: "api.github.com", Reason: ReasonAllowlist}
	if got != want {
		t.Fatalf("AdmitEgress() = %+v, want %+v", got, want)
	}
}

func TestAdmitEgressAllowsListedName(t *testing.T) {
	cases := []struct {
		host  string
		allow []string
		want  Decision
	}{
		{"API.GitHub.com.", githubAllow, Decision{Verdict: VerdictAllow, Host: "api.github.com", Reason: ReasonListed}},
		{"api.github.com:443", githubAllow, Decision{Verdict: VerdictAllow, Host: "api.github.com", Port: "443", Reason: ReasonListed}},
		{"api.github.com", []string{"API.GitHub.com."}, Decision{Verdict: VerdictAllow, Host: "api.github.com", Reason: ReasonListed}},
		{"pypi.org", []string{"api.github.com", "pypi.org"}, Decision{Verdict: VerdictAllow, Host: "pypi.org", Reason: ReasonListed}},
	}
	for _, c := range cases {
		got, err := AdmitEgress(c.host, c.allow)
		if err != nil {
			t.Fatalf("AdmitEgress(%q) error = %v", c.host, err)
		}
		if got != c.want {
			t.Errorf("AdmitEgress(%q) = %+v, want %+v", c.host, got, c.want)
		}
	}
}

func TestAdmitEgressParentDoesNotCoverChild(t *testing.T) {
	got, err := AdmitEgress("api.github.com", []string{"github.com"})
	if err != nil {
		t.Fatalf("AdmitEgress() error = %v", err)
	}
	want := Decision{Verdict: VerdictRefuse, Host: "api.github.com", Reason: ReasonAllowlist}
	if got != want {
		t.Fatalf("AdmitEgress() = %+v, want %+v", got, want)
	}
}

func TestAdmitEgressRefuses(t *testing.T) {
	cases := []struct {
		host, wantHost, wantPort, reason string
	}{
		{"evil.github.com", "evil.github.com", "", ReasonAllowlist},
		{"api.github.com.evil.com", "api.github.com.evil.com", "", ReasonAllowlist},
		{"github.com", "github.com", "", ReasonAllowlist},

		{"localhost", "localhost", "", ReasonHost},
		{"localhost:80", "localhost", "80", ReasonHost},
		{"foo.localhost", "foo.localhost", "", ReasonHost},
		{"host.docker.internal", "host.docker.internal", "", ReasonHost},
		{"brain", "brain", "", ReasonHost},
		{"postgres.internal", "postgres.internal", "", ReasonHost},
		{"printer.local", "printer.local", "", ReasonHost},
		{"nas.home.arpa", "nas.home.arpa", "", ReasonHost},
		{"metadata.google.internal", "metadata.google.internal", "", ReasonHost},
		{"localhost.localdomain", "localhost.localdomain", "", ReasonHost},

		{"127.0.0.1", "127.0.0.1", "", ReasonAddress},
		{"127.0.0.1:5432", "127.0.0.1", "5432", ReasonAddress},
		{"[::1]:8080", "::1", "8080", ReasonAddress},
		{"0.0.0.0", "0.0.0.0", "", ReasonAddress},
		{"169.254.169.254", "169.254.169.254", "", ReasonAddress},
		{"::ffff:127.0.0.1", "::ffff:127.0.0.1", "", ReasonAddress},
		{"127.1", "127.1", "", ReasonAddress},
		{"0177.0.0.1", "0177.0.0.1", "", ReasonAddress},
		{"0x7f.0.0.1", "0x7f.0.0.1", "", ReasonAddress},
		{"0X7F000001", "0x7f000001", "", ReasonAddress},
		{"2130706433", "2130706433", "", ReasonAddress},
		{"140.82.112.6", "140.82.112.6", "", ReasonAddress},
	}
	for _, c := range cases {
		got, err := AdmitEgress(c.host, githubAllow)
		if err != nil {
			t.Fatalf("AdmitEgress(%q) error = %v", c.host, err)
		}
		want := Decision{Verdict: VerdictRefuse, Host: c.wantHost, Port: c.wantPort, Reason: c.reason}
		if got != want {
			t.Errorf("AdmitEgress(%q) = %+v, want %+v", c.host, got, want)
		}
	}
}
