package domain

import (
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFreshWindow(t *testing.T) {
	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if !Fresh(start, start.Add(StepUpWindow)) {
		t.Fatal("boundary is outside the window")
	}
	if Fresh(start, start.Add(StepUpWindow+time.Nanosecond)) {
		t.Fatal("past the window is still fresh")
	}
}

func TestClientIP(t *testing.T) {
	proxy := mustCIDR(t, "203.0.113.8/32")
	got := ClientIP("203.0.113.8:443", "198.51.100.4, 203.0.113.8", []*net.IPNet{proxy})
	if got != "198.51.100.4" {
		t.Fatalf("forwarded = %s", got)
	}
	got = ClientIP("198.51.100.9:443", "203.0.113.8", []*net.IPNet{proxy})
	if got != "198.51.100.9" {
		t.Fatalf("untrusted connection used the header: %s", got)
	}
}

func TestRefillAndRetry(t *testing.T) {
	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if got := Refill(0, start, start.Add(6*time.Second), ThrottleBurst, ThrottlePerSecond); got != 1 {
		t.Fatalf("refill = %v, want 1", got)
	}
	if got := Refill(ThrottleBurst, start, start.Add(time.Hour), ThrottleBurst, ThrottlePerSecond); got != ThrottleBurst {
		t.Fatalf("refill over the burst = %v", got)
	}
	if RetryAfter(0, ThrottlePerSecond) != 6*time.Second {
		t.Fatalf("retry = %s", RetryAfter(0, ThrottlePerSecond))
	}
}

func TestPasskeyName(t *testing.T) {
	icloud := uuid.MustParse("fbfc3007-154e-4ecc-8c0b-6e020557d7bd")
	if PasskeyName(icloud) != "iCloud Keychain" {
		t.Fatalf("name = %s", PasskeyName(icloud))
	}
	if PasskeyName(uuid.Nil) != "Passkey" {
		t.Fatalf("unknown = %s", PasskeyName(uuid.Nil))
	}
}

func FuzzParseLinkToken(f *testing.F) {
	_, text, err := NewToken()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(text)
	f.Fuzz(func(t *testing.T, s string) {
		token, err := ParseLinkToken(s)
		if err != nil {
			return
		}
		if EncodeToken(token) != s {
			t.Fatalf("round trip %q", s)
		}
	})
}

func FuzzParseChallengeID(f *testing.F) {
	_, text, err := NewToken()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(text)
	f.Fuzz(func(t *testing.T, s string) {
		token, err := ParseChallengeID(s)
		if err != nil {
			return
		}
		if EncodeToken(token) != s {
			t.Fatalf("round trip %q", s)
		}
	})
}

func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return network
}
