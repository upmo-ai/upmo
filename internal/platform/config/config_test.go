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
	cfg, err := load(lookupFrom(nil))
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
}

func TestLoadFromEnv(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
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
	if _, err := load(lookupFrom(map[string]string{"UPMO_SHUTDOWN_TIMEOUT": "soon"})); err == nil {
		t.Fatal("expected an error for an invalid duration")
	}
}
