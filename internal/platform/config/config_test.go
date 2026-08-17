package config

import (
	"testing"
	"time"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 10s", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.SessionIdle != 24*time.Hour || cfg.SessionMaxAge != 14*24*time.Hour {
		t.Errorf("session timeouts = %s %s", cfg.SessionIdle, cfg.SessionMaxAge)
	}
	if cfg.PublicURL != "http://localhost:5173" || cfg.WebAuthnRPID != "localhost" {
		t.Errorf("public URL = %s rp = %s", cfg.PublicURL, cfg.WebAuthnRPID)
	}
	if cfg.ServiceKey != "" || cfg.ServiceKeyPrevious != "" {
		t.Errorf("service keys = %q %q", cfg.ServiceKey, cfg.ServiceKeyPrevious)
	}
}

func TestLoadServiceKeys(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL":         "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":           "http://localhost:5173",
		"UPMO_SERVICE_KEY":          "upmo_svc_current",
		"UPMO_SERVICE_KEY_PREVIOUS": "upmo_svc_previous",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceKey != "upmo_svc_current" || cfg.ServiceKeyPrevious != "upmo_svc_previous" {
		t.Fatalf("service keys = %q %q", cfg.ServiceKey, cfg.ServiceKeyPrevious)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	if _, err := load(lookupFrom(nil)); err == nil {
		t.Fatal("missing database URL was accepted")
	}
}

func TestLoadRejectsIdleLongerThanMaxAge(t *testing.T) {
	_, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL":         "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":           "http://localhost:5173",
		"UPMO_SESSION_IDLE_TIMEOUT": "48h",
		"UPMO_SESSION_MAX_AGE":      "24h",
	}))
	if err == nil {
		t.Fatal("idle longer than max age was accepted")
	}
}

func TestLoadFromEnv(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL":     "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":       "http://localhost:5173",
		"UPMO_LISTEN_ADDR":      "127.0.0.1:9000",
		"UPMO_SHUTDOWN_TIMEOUT": "3s",
		"UPMO_LOG_LEVEL":        "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" || cfg.ShutdownTimeout != 3*time.Second || cfg.LogLevel != "debug" {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	if _, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL":     "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":       "http://localhost:5173",
		"UPMO_SHUTDOWN_TIMEOUT": "soon",
	})); err == nil {
		t.Fatal("expected an error for an invalid duration")
	}
}
