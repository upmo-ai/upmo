package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/testutil/pgtls"
)

func repeated(prefix string, b byte) string {
	return prefix + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

func TestRunRequiresWorkerWithSandboxDatabase(t *testing.T) {
	keys(t)
	t.Setenv("UPMO_ENV", "development")
	t.Setenv("UPMO_DATABASE_SANDBOX_URL", pgtls.URL(t, "upmo_sandbox"))
	err := run()
	if err == nil || !strings.Contains(err.Error(), "UPMO_WORKER_URL") || !strings.Contains(err.Error(), "UPMO_WORKER_SANDBOXD_KEY") {
		t.Fatalf("run err = %v, want worker configuration refusal", err)
	}
}

// keys sets a fresh-looking sandboxd and editor key, so each case changes
// one variable. The values are not uniform, so the production guard passes.
func keys(t *testing.T) {
	t.Helper()
	t.Setenv("UPMO_ENV", "")
	t.Setenv("UPMO_SANDBOXD_KEY", "upmo_svc_sandboxd_"+base64.RawStdEncoding.EncodeToString([]byte("sandboxd-key-for-the-main-test!!")))
	t.Setenv("UPMO_EDITOR_KEY", "upmo_edk_"+base64.RawStdEncoding.EncodeToString([]byte("editor-key-for-the-main-test!!!!")))
}

// run() reads the real environment, so its guard runs on t.Setenv values. The
// refusal lands in LoadSandboxd, before any driver or listener opens. An
// empty UPMO_ENV is production, like an unset one.
func TestRunRefusesDevelopmentKeysInProduction(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"UPMO_SANDBOXD_KEY", repeated("upmo_svc_sandboxd_", 's')},
		{"UPMO_EDITOR_KEY", repeated("upmo_edk_", 'e')},
		{"UPMO_METRICS_TOKEN", repeated("upmo_mtr_", 't')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys(t)
			t.Setenv(tc.name, tc.value)
			err := run()
			if err == nil || !strings.Contains(err.Error(), tc.name+" uses a development or weak key") {
				t.Fatalf("run err = %v", err)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Fatalf("run err holds the key: %v", err)
			}
		})
	}
}

func TestRunRefusesBadKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   map[string]string
		unset string
		want  string
	}{
		{
			name:  "service master set, editor key unset",
			env:   map[string]string{"UPMO_SERVICE_KEY": repeated("upmo_svc_", 'k')},
			unset: "UPMO_EDITOR_KEY",
			want:  "config: UPMO_SERVICE_KEY is not read; set UPMO_SANDBOXD_KEY and UPMO_EDITOR_KEY",
		},
		{
			name: "previous service master set empty",
			env:  map[string]string{"UPMO_SERVICE_KEY_PREVIOUS": ""},
			want: "config: UPMO_SERVICE_KEY is not read; set UPMO_SANDBOXD_KEY and UPMO_EDITOR_KEY",
		},
		{
			name:  "editor key unset",
			unset: "UPMO_EDITOR_KEY",
			want:  "config: UPMO_EDITOR_KEY is required",
		},
		{
			name: "sandboxd key unset",
			env:  map[string]string{"UPMO_SANDBOXD_KEY": ""},
			want: "config: UPMO_SANDBOXD_KEY is required",
		},
		{
			name: "gateway key as sandboxd key",
			env:  map[string]string{"UPMO_SANDBOXD_KEY": repeated("upmo_svc_gateway_", 'q')},
			want: "config: UPMO_SANDBOXD_KEY: svcsig: key is not a sandboxd key",
		},
		{
			name: "raw master as sandboxd key",
			env:  map[string]string{"UPMO_SANDBOXD_KEY": repeated("upmo_svc_", 'q')},
			want: "config: UPMO_SANDBOXD_KEY: svcsig: a raw service master is not accepted; give the derived sandboxd key",
		},
		{
			name: "service master as editor key",
			env:  map[string]string{"UPMO_EDITOR_KEY": repeated("upmo_svc_", 'q')},
			want: "config: UPMO_EDITOR_KEY: editorjwt: key is not upmo_edk_ followed by base64 of 32 bytes",
		},
		{
			name: "metrics without token",
			env:  map[string]string{"UPMO_METRICS_ADDR": "127.0.0.1:9091"},
			want: "config: UPMO_METRICS_TOKEN is required when UPMO_METRICS_ADDR is set",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if tc.unset != "" {
				_ = os.Unsetenv(tc.unset) // keys(t) set it, so t.Setenv restores it after
			}
			if err := run(); err == nil || err.Error() != tc.want {
				t.Fatalf("run err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Sandboxd never holds the privileged role: with UPMO_DATABASE_URL set it
// stops before any driver, listener or pool opens, and the error names it.
func TestRunRefusesTheOwnerDatabaseURL(t *testing.T) {
	keys(t)
	t.Setenv("UPMO_DATABASE_URL", "postgres://upmo:owner-secret@/upmo?host=/var/run/postgresql")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "UPMO_DATABASE_URL") || strings.Contains(err.Error(), "owner-secret") {
		t.Fatalf("run err = %v, want a refusal naming UPMO_DATABASE_URL", err)
	}
}

// Invariant 2 and 3 through the real entry point: sandboxd stops on a master
// key, current or previous, even empty, and on another caller's worker key,
// before any driver, listener or pool opens.
func TestRunRefusesTheMasterKeyAndForeignWorkerKeys(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"UPMO_MASTER_KEY", repeated("", 'm'), "config: UPMO_MASTER_KEY is refused"},
		{"UPMO_MASTER_KEY", "", "config: UPMO_MASTER_KEY is refused"},
		{"UPMO_MASTER_KEY_PREVIOUS_k0", repeated("", 'm'), "config: UPMO_MASTER_KEY is refused"},
		{"UPMO_MASTER_KEY_PREVIOUS_k0", "", "config: UPMO_MASTER_KEY is refused"},
		{"UPMO_WORKER_BRAIN_KEY", repeated("upmo_svc_worker-brain_", 'b'), "config: UPMO_WORKER_BRAIN_KEY must not be set for sandboxd"},
		{"UPMO_WORKER_GATEWAY_KEY", repeated("upmo_svc_worker-gateway_", 'g'), "config: UPMO_WORKER_GATEWAY_KEY must not be set for sandboxd"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			keys(t)
			t.Setenv(tc.name, tc.value)
			if err := run(); err == nil || err.Error() != tc.want {
				t.Fatalf("run err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Sandboxd checks the worker, signed, at start when it is configured, and
// stops when it does not answer.
func TestRunNeedsAReadyWorkerWhenConfigured(t *testing.T) {
	keys(t)
	t.Setenv("UPMO_ENV", "development")
	t.Setenv("UPMO_WORKER_URL", "http://127.0.0.1:1")
	t.Setenv("UPMO_WORKER_SANDBOXD_KEY", "upmo_svc_worker-sandboxd_"+base64.RawStdEncoding.EncodeToString([]byte("worker-sandboxd-key-for-the-test")))
	if err := run(); err == nil || err.Error() != "config: worker is not ready" {
		t.Fatalf("run err = %v, want config: worker is not ready", err)
	}
}
