package main

import (
	"context"
	"strings"
	"testing"
)

func TestOpenPoolScrubsPassword(t *testing.T) {
	const marker = "owner-secret-marker"

	cases := []struct {
		name     string
		rawURL   string
		wantHost string
	}{
		{
			name:   "malformed port",
			rawURL: "postgres://upmo:" + marker + "@127.0.0.1:notaport/upmo",
		},
		{
			name:   "invalid scheme without host",
			rawURL: "://upmo:" + marker + "@",
		},
		{
			name:   "empty host with userinfo and invalid param",
			rawURL: "postgres://upmo:" + marker + "@?connect_timeout=notanint",
		},
		{
			name:   "keyword DSN without host",
			rawURL: "host=127.0.0.1 user=upmo password=" + marker + " port=notaport",
		},
		{
			name:     "valid host with invalid param",
			rawURL:   "postgres://upmo:" + marker + "@127.0.0.1:5432/upmo?sslmode=bogus",
			wantHost: "127.0.0.1:5432",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, err := openPool(context.Background(), tc.rawURL)
			if pool != nil {
				pool.Close()
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			msg := err.Error()
			if strings.Contains(msg, marker) {
				t.Fatalf("error leaked password marker: %q", msg)
			}
			if strings.Contains(msg, "upmo:") {
				t.Fatalf("error leaked userinfo: %q", msg)
			}
			const wantPrefix = "gateway: UPMO_DATABASE_URL could not be opened"
			if !strings.Contains(msg, wantPrefix) {
				t.Fatalf("error = %q, want prefix %q", msg, wantPrefix)
			}
			if tc.wantHost != "" {
				if !strings.Contains(msg, "(host "+tc.wantHost+")") {
					t.Fatalf("error = %q, want host %q", msg, tc.wantHost)
				}
			} else if strings.Contains(msg, "(host ") {
				t.Fatalf("error = %q, did not expect host", msg)
			}
		})
	}
}

func TestPoolURLErrorKeepsOnlyHostOnClosedPortURL(t *testing.T) {
	const marker = "owner-secret-marker"
	err := poolURLError("UPMO_DATABASE_URL", "postgres://upmo:"+marker+"@127.0.0.1:59999/upmo")
	msg := err.Error()
	if strings.Contains(msg, marker) || strings.Contains(msg, "upmo:") {
		t.Fatalf("error leaked credentials: %q", msg)
	}
	const want = "gateway: UPMO_DATABASE_URL could not be opened (host 127.0.0.1:59999)"
	if msg != want {
		t.Fatalf("error = %q, want %q", msg, want)
	}
}
