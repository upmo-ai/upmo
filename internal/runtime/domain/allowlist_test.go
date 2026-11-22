package domain

import (
	"errors"
	"reflect"
	"testing"
)

// sameSlice reports whether a and b share length, capacity and backing array.
func sameSlice(a, b []string) bool {
	return len(a) == len(b) && cap(a) == cap(b) && (len(a) == 0 || &a[0] == &b[0])
}

func wantRefusedAllowlist(t *testing.T, host string, list []string) {
	t.Helper()
	got, err := AdmitEgress(host, list)
	if err != nil || got.Verdict != VerdictRefuse || got.Reason != ReasonAllowlist {
		t.Fatalf("AdmitEgress(%q, %q) = %+v, %v, want refuse %q", host, list, got, err, ReasonAllowlist)
	}
}

func wantErrEgress(t *testing.T, list, got []string, change Change, err error) {
	t.Helper()
	if !errors.Is(err, ErrEgress) {
		t.Fatalf("error = %v, want %v", err, ErrEgress)
	}
	if change != (Change{}) {
		t.Fatalf("change = %+v, want zero Change", change)
	}
	if !sameSlice(got, list) {
		t.Fatalf("list = %q, want the caller slice %q", got, list)
	}
}

func TestOpenListRefusesPublicName(t *testing.T) {
	list := OpenList()
	if list != nil {
		t.Fatalf("OpenList() = %q, want nil", list)
	}
	wantRefusedAllowlist(t, "api.github.com", list)
}

func TestGrantNormalizesHostAndAllows(t *testing.T) {
	backing := []string{"example.com", "spare", "spare"}
	want := append([]string(nil), backing...)
	list := backing[:1]

	got, change, err := Grant(list, "ada", " API.GitHub.com. ")
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	if wantChange := (Change{Person: "ada", Op: OpGrant, Host: "api.github.com"}); change != wantChange {
		t.Fatalf("Grant() change = %+v, want %+v", change, wantChange)
	}
	if !reflect.DeepEqual(got, []string{"example.com", "api.github.com"}) {
		t.Fatalf("Grant() list = %q", got)
	}
	if !reflect.DeepEqual(backing, want) {
		t.Fatalf("Grant() backing = %q, want %q", backing, want)
	}

	d, err := AdmitEgress("api.github.com", got)
	if err != nil || d.Verdict != VerdictAllow || d.Reason != ReasonListed {
		t.Fatalf("AdmitEgress() = %+v, %v, want allow %q", d, err, ReasonListed)
	}
}

func TestGrantRejectsIllegalHost(t *testing.T) {
	list := []string{"example.com"}
	for _, host := range []string{"localhost", "127.0.0.1", "brain", "*.example.com", "api.github.com:443"} {
		got, change, err := Grant(list, "ada", host)
		wantErrEgress(t, list, got, change, err)
	}
	if !reflect.DeepEqual(list, []string{"example.com"}) {
		t.Fatalf("list = %q, want unchanged", list)
	}
}

func TestGrantRejectsBadPerson(t *testing.T) {
	list := []string{"example.com"}
	for _, person := range []string{"", "   ", "a b", "a\tb", "a/b", string(make([]byte, 65))} {
		got, change, err := Grant(list, person, "api.github.com")
		wantErrEgress(t, list, got, change, err)
	}
}

func TestGrantParentDoesNotAllowChild(t *testing.T) {
	list, _, err := Grant(OpenList(), "ada", "github.com")
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	wantRefusedAllowlist(t, "api.github.com", list)
}

func TestGrantAppendsAndRejectsDuplicate(t *testing.T) {
	first, _, err := Grant(OpenList(), "ada", "github.com")
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	second, _, err := Grant(first, "ada", "api.github.com")
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	if !reflect.DeepEqual(second, []string{"github.com", "api.github.com"}) {
		t.Fatalf("Grant() list = %q", second)
	}

	got, change, err := Grant(second, "ada", "GitHub.com")
	wantErrEgress(t, second, got, change, err)
}

func TestRevoke(t *testing.T) {
	list := []string{"github.com", "api.github.com"}

	got, change, err := Revoke(list, "ada", "github.com")
	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if wantChange := (Change{Person: "ada", Op: OpRevoke, Host: "github.com"}); change != wantChange {
		t.Fatalf("Revoke() change = %+v, want %+v", change, wantChange)
	}
	if !reflect.DeepEqual(got, []string{"api.github.com"}) {
		t.Fatalf("Revoke() list = %q", got)
	}
	if !reflect.DeepEqual(list, []string{"github.com", "api.github.com"}) {
		t.Fatalf("Revoke() changed the caller list to %q", list)
	}

	missing, change, err := Revoke(got, "ada", "example.com")
	wantErrEgress(t, got, missing, change, err)

	empty, _, err := Revoke(got, "ada", "api.github.com")
	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	wantRefusedAllowlist(t, "api.github.com", empty)
}

func TestNoteCopiesDecision(t *testing.T) {
	for _, d := range []Decision{
		{Verdict: VerdictAllow, Host: "api.github.com", Port: "443", Reason: ReasonListed},
		{Verdict: VerdictRefuse, Host: "127.0.0.1", Reason: ReasonAddress},
	} {
		got, err := Note(d)
		want := Request{Host: d.Host, Port: d.Port, Verdict: d.Verdict, Reason: d.Reason}
		if err != nil || got != want {
			t.Errorf("Note(%+v) = %+v, %v, want %+v", d, got, err, want)
		}
	}
}

func TestNoteRejectsMalformedDecision(t *testing.T) {
	for _, d := range []Decision{
		{Verdict: VerdictAllow, Host: "api.github.com/secret", Reason: ReasonListed},
		{Verdict: VerdictAllow, Host: "api.github.com?q=1", Reason: ReasonListed},
		{Verdict: VerdictAllow, Host: "api.github.com", Reason: ReasonAllowlist},
		{Verdict: VerdictRefuse, Host: "api.github.com", Reason: ReasonListed},
		{Verdict: "maybe", Host: "api.github.com", Reason: ReasonAllowlist},
		{Verdict: VerdictRefuse, Host: "api.github.com", Reason: "other"},
		{Verdict: VerdictRefuse, Reason: ReasonAllowlist},
		{Verdict: VerdictAllow, Host: "api.github.com", Port: "0443", Reason: ReasonListed},
		{Verdict: VerdictAllow, Host: "api.github.com", Port: "65536", Reason: ReasonListed},
	} {
		got, err := Note(d)
		if !errors.Is(err, ErrEgress) || got != (Request{}) {
			t.Errorf("Note(%+v) = %+v, %v, want %v", d, got, err, ErrEgress)
		}
	}
}
