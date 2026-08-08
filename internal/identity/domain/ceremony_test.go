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
	if RetryAfter(-1, 1.0/30) != 60*time.Second {
		t.Fatalf("next token retry = %s", RetryAfter(-1, 1.0/30))
	}
}

func TestThrottleAddressSharesAnIPv6Prefix(t *testing.T) {
	one := ThrottleAddress("2001:db8:1:2::1")
	other := ThrottleAddress("2001:db8:1:2::abcd")
	if one != other {
		t.Fatalf("prefix = %s and %s", one, other)
	}
	if ThrottleAddress("2001:db8:1:3::1") == one {
		t.Fatal("a different prefix shared the bucket")
	}
	if ThrottleAddress("198.51.100.9") != "198.51.100.9" {
		t.Fatalf("ipv4 = %s", ThrottleAddress("198.51.100.9"))
	}
	if ThrottleAddress("not-an-ip") != "not-an-ip" {
		t.Fatal("a non-address was rewritten")
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
