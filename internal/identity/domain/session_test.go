package domain

import (
	"testing"
	"time"
)

func TestSessionEdges(t *testing.T) {
	start := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	idle := IdleTimeout
	expires := start.Add(MaxAge)

	t.Run("idle", func(t *testing.T) {
		if Ended(start, expires, start.Add(idle-time.Nanosecond), idle) {
			t.Fatal("ended inside the idle window")
		}
		if !Ended(start, expires, start.Add(idle), idle) {
			t.Fatal("still open on the idle boundary")
		}
	})
	t.Run("absolute", func(t *testing.T) {
		last := expires.Add(-time.Hour)
		if Ended(last, expires, expires.Add(-time.Nanosecond), idle) {
			t.Fatal("ended before the absolute expiry")
		}
		if !Ended(last, expires, expires, idle) {
			t.Fatal("still open on the absolute expiry")
		}
	})
	t.Run("touch", func(t *testing.T) {
		if TouchDue(start, start.Add(TouchInterval-time.Nanosecond)) {
			t.Fatal("touch inside the minute")
		}
		if !TouchDue(start, start.Add(TouchInterval)) {
			t.Fatal("no touch on the minute boundary")
		}
	})
}

func TestTokenRoundTrip(t *testing.T) {
	token, text, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(text) != 43 {
		t.Fatalf("text length = %d, want 43", len(text))
	}
	parsed, err := ParseToken(text)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != token {
		t.Fatal("parsed token differs")
	}
	if string(Hash(token)) == string(token[:]) {
		t.Fatal("hash equals the token")
	}
}

func TestParseTokenRejects(t *testing.T) {
	_, text, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", text + "a", text[:42], text[:42] + "+", "===="} {
		if _, err := ParseToken(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func FuzzParseToken(f *testing.F) {
	_, text, err := NewToken()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(text)
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		token, err := ParseToken(s)
		if err != nil {
			return
		}
		if EncodeToken(token) != s {
			t.Fatalf("EncodeToken(ParseToken(%q)) = %q", s, EncodeToken(token))
		}
	})
}
