package domain

import (
	"errors"
	"testing"
	"time"
)

const (
	tokenUser    = "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"
	tokenTask    = "7a2b3c4d-5e6f-4a71-9b8c-0d1e2f3a4b5c"
	tokenSession = "8b3c4d5e-6f7a-4b82-8c9d-1e2f3a4b5c6d"
)

var tokenNow = time.Date(2026, time.October, 31, 12, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))

func TestIssueCanonicalisesAndExpires(t *testing.T) {
	if TokenTTL != 15*time.Minute {
		t.Fatalf("TokenTTL = %v, want %v", TokenTTL, 15*time.Minute)
	}
	got, err := Issue(" 6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B ", tokenTask, tokenSession, tokenNow)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	want := Token{User: tokenUser, Task: tokenTask, Session: tokenSession, Expires: tokenNow.UTC().Add(TokenTTL)}
	if got != want {
		t.Fatalf("Issue() = %+v, want %+v", got, want)
	}
}

func TestIssueRefuses(t *testing.T) {
	cases := []struct {
		name                string
		user, task, session string
		now                 time.Time
	}{
		{"zero time", tokenUser, tokenTask, tokenSession, time.Time{}},
		{"nil uuid", "00000000-0000-0000-0000-000000000000", tokenTask, tokenSession, tokenNow},
		{"blank id", tokenUser, " ", tokenSession, tokenNow},
		{"session equals task", tokenUser, tokenTask, tokenTask, tokenNow},
	}
	for _, c := range cases {
		got, err := Issue(c.user, c.task, c.session, c.now)
		if !errors.Is(err, ErrEditor) {
			t.Fatalf("Issue(%s) error = %v, want %v", c.name, err, ErrEditor)
		}
		if got != (Token{}) {
			t.Fatalf("Issue(%s) = %+v, want zero Token", c.name, got)
		}
	}
}

func TestAcceptExpiry(t *testing.T) {
	token, err := Issue(tokenUser, tokenTask, tokenSession, tokenNow)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	cases := []struct {
		now     time.Time
		wantErr error
	}{
		{token.Expires.Add(-time.Nanosecond), nil},
		{token.Expires, ErrEditor},
		{token.Expires.Add(time.Nanosecond), ErrEditor},
	}
	for _, c := range cases {
		if err := Accept(token, tokenUser, tokenTask, tokenSession, c.now); !errors.Is(err, c.wantErr) {
			t.Fatalf("Accept(%v) error = %v, want %v", c.now, err, c.wantErr)
		}
	}
}

func TestAcceptRefusesMismatch(t *testing.T) {
	token, err := Issue(tokenUser, tokenTask, tokenSession, tokenNow)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	other := "9c4d5e6f-7a8b-4c93-9dae-2f3a4b5c6d7e"
	cases := []struct {
		name                string
		user, task, session string
	}{
		{"user", other, tokenTask, tokenSession},
		{"task", tokenUser, other, tokenSession},
		{"session", tokenUser, tokenTask, other},
	}
	for _, c := range cases {
		if err := Accept(token, c.user, c.task, c.session, tokenNow); !errors.Is(err, ErrEditor) {
			t.Fatalf("Accept(%s) error = %v, want %v", c.name, err, ErrEditor)
		}
	}
}

func TestSeparateOrigin(t *testing.T) {
	cases := []struct {
		app, editor string
		wantErr     error
	}{
		{"https://app.example", "https://editor.example", nil},
		{"http://127.0.0.1:5173", "http://127.0.0.1:8081", nil},
		{"https://app.example/", "https://app.example", ErrEditor},
		{"https://APP.example", "https://app.example:443", ErrEditor},
		{"", "https://editor.example", ErrEditor},
		{"https://app.example", "/editor", ErrEditor},
		{"https://app.example", "javascript:alert(1)", ErrEditor},
		{"https://app.example", "https://person:secret@editor.example", ErrEditor},
		{"https://app.example", "https://app.example:0443", ErrEditor},
		{"https://[::1]:8081", "https://[::1]:8082", nil},
	}
	for _, c := range cases {
		if err := SeparateOrigin(c.app, c.editor); !errors.Is(err, c.wantErr) {
			t.Fatalf("SeparateOrigin(%q, %q) error = %v, want %v", c.app, c.editor, err, c.wantErr)
		}
	}
}

func TestGallery(t *testing.T) {
	cases := []struct {
		host    string
		wantErr error
	}{
		{"open-vsx.org", nil},
		{"Open-VSX.org", nil},
		{"www.open-vsx.org", ErrEditor},
		{"marketplace.visualstudio.com", ErrEditor},
		{"localhost", ErrEditor},
		{"127.0.0.1", ErrEditor},
		{"", ErrEditor},
		{"open-vsx.org:443", ErrEditor},
	}
	for _, c := range cases {
		if err := Gallery(c.host); !errors.Is(err, c.wantErr) {
			t.Fatalf("Gallery(%q) error = %v, want %v", c.host, err, c.wantErr)
		}
	}
}
