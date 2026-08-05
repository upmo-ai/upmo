// Package config loads process configuration from UPMO_ environment variables.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds the settings brain reads at start.
type Config struct {
	// DatabaseURL is the Postgres URL (UPMO_DATABASE_URL). It is required.
	DatabaseURL string
	// SessionIdle ends a session with no requests (UPMO_SESSION_IDLE_TIMEOUT).
	SessionIdle time.Duration
	// SessionMaxAge is the absolute session lifetime (UPMO_SESSION_MAX_AGE).
	SessionMaxAge time.Duration
	// ListenAddr is the HTTP listen address (UPMO_LISTEN_ADDR).
	ListenAddr string
	// ShutdownTimeout bounds graceful shutdown (UPMO_SHUTDOWN_TIMEOUT).
	ShutdownTimeout time.Duration
	// LogLevel is one of debug, info, warn, error (UPMO_LOG_LEVEL).
	LogLevel string
}

// Load reads the configuration from the environment and applies defaults.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		ListenAddr:      ":8080",
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        "info",
		SessionIdle:     24 * time.Hour,
		SessionMaxAge:   14 * 24 * time.Hour,
	}
	v, ok := lookup("UPMO_DATABASE_URL")
	if !ok || v == "" {
		return Config{}, fmt.Errorf("config: UPMO_DATABASE_URL is required")
	}
	cfg.DatabaseURL = v
	if v, ok := lookup("UPMO_SESSION_IDLE_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: UPMO_SESSION_IDLE_TIMEOUT: %w", err)
		}
		cfg.SessionIdle = d
	}
	if v, ok := lookup("UPMO_SESSION_MAX_AGE"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: UPMO_SESSION_MAX_AGE: %w", err)
		}
		cfg.SessionMaxAge = d
	}
	if cfg.SessionIdle > cfg.SessionMaxAge {
		return Config{}, fmt.Errorf("config: session idle timeout is longer than the max age")
	}
	if v, ok := lookup("UPMO_LISTEN_ADDR"); ok && v != "" {
		cfg.ListenAddr = v
	}
	if v, ok := lookup("UPMO_SHUTDOWN_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: UPMO_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	if v, ok := lookup("UPMO_LOG_LEVEL"); ok && v != "" {
		cfg.LogLevel = v
	}
	return cfg, nil
}
