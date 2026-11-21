package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// run() reads the real environment, so its guard runs on t.Setenv values. An
// empty UPMO_ENV is production, like an unset one, and every refusal lands
// before any pool is opened.
func TestRunRefusesDevelopmentValuesInProduction(t *testing.T) {
	freshKey := func(t *testing.T) string {
		t.Helper()
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(raw)
	}
	freshDBURL := func(t *testing.T) string {
		t.Helper()
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		return "postgres://upmo:" + hex.EncodeToString(raw) + "@postgres:5432/upmo?sslmode=disable"
	}
	base := func(t *testing.T) {
		t.Helper()
		t.Setenv("UPMO_ENV", "")
		t.Setenv("UPMO_DATABASE_URL", freshDBURL(t))
		t.Setenv("UPMO_PUBLIC_URL", "http://127.0.0.1:8082")
		t.Setenv("UPMO_MASTER_KEY", freshKey(t))
	}
	for _, tc := range []struct {
		name, key, value, want string
	}{
		{
			name:  "master key",
			key:   "UPMO_MASTER_KEY",
			value: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32)),
			want:  "UPMO_MASTER_KEY uses a development or weak key",
		},
		{
			name:  "service key",
			key:   "UPMO_SERVICE_KEY",
			value: "upmo_svc_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'g'}, 32)),
			want:  "UPMO_SERVICE_KEY uses a development or weak key",
		},
		{
			name:  "credential",
			key:   "UPMO_CREDENTIAL_house",
			value: "local-dev-not-a-provider-key",
			want:  "UPMO_CREDENTIAL_house uses a development value",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base(t)
			t.Setenv(tc.key, tc.value)
			err := run()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run err = %v, want it to say %q", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Fatalf("run err holds the value: %v", err)
			}
		})
	}
	t.Run("database url", func(t *testing.T) {
		base(t)
		t.Setenv("UPMO_DATABASE_URL", "postgres://upmo:upmo@postgres:5432/upmo?sslmode=disable")
		err := run()
		if err == nil || !strings.Contains(err.Error(), "UPMO_DATABASE_URL uses a development or empty password") {
			t.Fatalf("run err = %v", err)
		}
		if strings.Contains(err.Error(), "upmo:upmo") {
			t.Fatalf("run err holds the credentials: %v", err)
		}
	})
	t.Run("master key required", func(t *testing.T) {
		base(t)
		t.Setenv("UPMO_MASTER_KEY", "")
		t.Setenv("UPMO_SERVICE_KEY", "upmo_svc_"+freshKey(t))
		err := run()
		if err == nil || err.Error() != "config: UPMO_MASTER_KEY is required; set a key" {
			t.Fatalf("run err = %v", err)
		}
	})
}

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

func TestOpenPoolScrubsUserAndHostOnConnectFailure(t *testing.T) {
	const user = "connect-user-marker"
	pool, err := openPool(context.Background(), "postgres://"+user+":pw-marker@127.0.0.1:1/upmo?connect_timeout=2")
	if pool != nil {
		pool.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, leak := range []string{user, "pw-marker", "127.0.0.1"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("error leaked %q: %q", leak, msg)
		}
	}
	const want = "gateway: UPMO_DATABASE_URL could not be opened"
	if msg != want {
		t.Fatalf("error = %q, want %q", msg, want)
	}
}
