package main

import (
	"context"
	"strings"
	"testing"
)

func TestDatabaseURLErrorScrubsPassword(t *testing.T) {
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

	sites := []struct {
		name    string
		envName string
		open    func(string) error
	}{
		{
			name:    "openOwnerPool",
			envName: "UPMO_DATABASE_URL",
			open: func(rawURL string) error {
				pool, err := openOwnerPool(context.Background(), rawURL)
				if pool != nil {
					pool.Close()
				}
				return err
			},
		},
		{
			name:    "parseListenConfig",
			envName: "UPMO_DATABASE_URL",
			open: func(rawURL string) error {
				_, err := parseListenConfig(rawURL)
				return err
			},
		},
		{
			name:    "openCommandPool",
			envName: "UPMO_DATABASE_URL",
			open: func(rawURL string) error {
				pool, err := openCommandPool(context.Background(), rawURL)
				if pool != nil {
					pool.Close()
				}
				return err
			},
		},
		{
			name:    "requestPool",
			envName: "UPMO_DATABASE_APP_URL",
			open: func(rawURL string) error {
				pool, err := requestPool(context.Background(), rawURL)
				if pool != nil {
					pool.Close()
				}
				return err
			},
		},
		{
			name:    "appPoolError",
			envName: "UPMO_DATABASE_APP_URL",
			open:    appPoolError,
		},
	}

	for _, site := range sites {
		for _, tc := range cases {
			t.Run(site.name+"/"+tc.name, func(t *testing.T) {
				err := site.open(tc.rawURL)
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
				wantPrefix := "brain: " + site.envName + " could not be opened"
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
}

func TestPoolURLErrorKeepsOnlyHostOnClosedPortURL(t *testing.T) {
	const marker = "owner-secret-marker"
	closedPortURL := "postgres://upmo:" + marker + "@127.0.0.1:59999/upmo"

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "owner",
			err:  poolURLError("UPMO_DATABASE_URL", closedPortURL),
			want: "brain: UPMO_DATABASE_URL could not be opened (host 127.0.0.1:59999)",
		},
		{
			name: "app",
			err:  appPoolError(closedPortURL),
			want: "brain: UPMO_DATABASE_APP_URL could not be opened (host 127.0.0.1:59999)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			if strings.Contains(msg, marker) || strings.Contains(msg, "upmo:") {
				t.Fatalf("error leaked credentials: %q", msg)
			}
			if msg != tc.want {
				t.Fatalf("error = %q, want %q", msg, tc.want)
			}
		})
	}
}
