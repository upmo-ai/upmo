package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/testutil/pgtls"
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
		return pgtls.URL(t, "upmo_gateway:"+hex.EncodeToString(raw))
	}
	base := func(t *testing.T) {
		t.Helper()
		t.Setenv("UPMO_ENV", "")
		t.Setenv("UPMO_DATABASE_GATEWAY_URL", freshDBURL(t))
		t.Setenv("UPMO_PUBLIC_URL", "http://127.0.0.1:8082")
		t.Setenv("UPMO_MASTER_KEY", freshKey(t))
		t.Setenv("UPMO_GATEWAY_KEY", "upmo_svc_gateway_"+freshKey(t))
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
			name:  "gateway key",
			key:   "UPMO_GATEWAY_KEY",
			value: "upmo_svc_gateway_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'g'}, 32)),
			want:  "UPMO_GATEWAY_KEY uses a development or weak key",
		},
		{
			name:  "metrics token",
			key:   "UPMO_METRICS_TOKEN",
			value: "upmo_mtr_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'t'}, 32)),
			want:  "UPMO_METRICS_TOKEN uses a development or weak key",
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
		t.Setenv("UPMO_DATABASE_GATEWAY_URL", pgtls.URL(t, "upmo_gateway:upmo_gateway"))
		err := run()
		if err == nil || !strings.Contains(err.Error(), "UPMO_DATABASE_GATEWAY_URL uses a development or empty password") {
			t.Fatalf("run err = %v", err)
		}
		if strings.Contains(err.Error(), "upmo_gateway:upmo_gateway") {
			t.Fatalf("run err holds the credentials: %v", err)
		}
	})
	t.Run("master key required", func(t *testing.T) {
		base(t)
		t.Setenv("UPMO_MASTER_KEY", "")
		err := run()
		if err == nil || err.Error() != "config: UPMO_MASTER_KEY is required; set a key" {
			t.Fatalf("run err = %v", err)
		}
	})
	// Each refusal lands in LoadGateway, before any pool opens, with the exact text.
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"service master set", map[string]string{"UPMO_SERVICE_KEY": "upmo_svc_" + freshKey(t)}, "config: UPMO_SERVICE_KEY is not read; set UPMO_GATEWAY_KEY"},
		{"service master set empty", map[string]string{"UPMO_SERVICE_KEY_PREVIOUS": ""}, "config: UPMO_SERVICE_KEY is not read; set UPMO_GATEWAY_KEY"},
		{"gateway key missing", map[string]string{"UPMO_GATEWAY_KEY": ""}, "config: UPMO_GATEWAY_KEY is required"},
		{"sandboxd key as gateway key", map[string]string{"UPMO_GATEWAY_KEY": "upmo_svc_sandboxd_" + freshKey(t)}, "config: UPMO_GATEWAY_KEY: svcsig: key is not a gateway key"},
		{"raw master as gateway key", map[string]string{"UPMO_GATEWAY_KEY": "upmo_svc_" + freshKey(t)}, "config: UPMO_GATEWAY_KEY: svcsig: a raw service master is not accepted; give the derived gateway key"},
		{"metrics token as gateway key", map[string]string{"UPMO_GATEWAY_KEY": "upmo_mtr_" + freshKey(t)}, "config: UPMO_GATEWAY_KEY: svcsig: key is not upmo_svc_gateway_ followed by base64 of 32 bytes"},
		{"metrics without token", map[string]string{"UPMO_METRICS_ADDR": "127.0.0.1:9090"}, "config: UPMO_METRICS_TOKEN is required when UPMO_METRICS_ADDR is set"},
		{"metrics with a service key as token", map[string]string{"UPMO_METRICS_ADDR": "127.0.0.1:9090", "UPMO_METRICS_TOKEN": "upmo_svc_gateway_" + freshKey(t)}, "config: UPMO_METRICS_TOKEN: metrics: token is not upmo_mtr_ followed by base64 of at least 32 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if err := run(); err == nil || err.Error() != tc.want {
				t.Fatalf("run err = %v, want %q", err, tc.want)
			}
		})
	}
}
