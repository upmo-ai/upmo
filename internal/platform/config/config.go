// Package config loads process configuration from UPMO_ environment variables.
package config

import (
	"fmt"
	"net"
	netmail "net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	// PublicURL is the browser origin (UPMO_PUBLIC_URL). It is required.
	PublicURL string
	// WebAuthnRPID is the passkey relying party id (UPMO_WEBAUTHN_RP_ID).
	WebAuthnRPID string
	// TrustedProxies are the proxies whose X-Forwarded-For is believed (UPMO_TRUSTED_PROXIES).
	TrustedProxies []*net.IPNet
	// ListenAddr is the HTTP listen address (UPMO_LISTEN_ADDR).
	ListenAddr string
	// ShutdownTimeout bounds graceful shutdown (UPMO_SHUTDOWN_TIMEOUT).
	ShutdownTimeout time.Duration
	// LogLevel is one of debug, info, warn, error (UPMO_LOG_LEVEL).
	LogLevel string
	// ServiceKey is the master HMAC key for process-to-process calls (UPMO_SERVICE_KEY).
	ServiceKey string
	// ServiceKeyPrevious is the previous master key during rotation (UPMO_SERVICE_KEY_PREVIOUS).
	ServiceKeyPrevious string
	// GatewayURL is the model gateway (UPMO_GATEWAY_URL). Empty until brain calls it.
	GatewayURL string
	// SMTP is the relay for invite and recover emails (UPMO_SMTP_*). Its Host
	// is empty when mail is unset, and links are then only copied.
	SMTP SMTP
}

// SMTP is the mail relay settings.
type SMTP struct {
	// Host is the relay host (UPMO_SMTP_HOST). It is required when any UPMO_SMTP_ value is set.
	Host string
	// Port is the relay port (UPMO_SMTP_PORT), 587 by default.
	Port int
	// User and Password log in to the relay (UPMO_SMTP_USER, UPMO_SMTP_PASSWORD). Both or neither.
	User     string
	Password string
	// From is the sender address (UPMO_SMTP_FROM). It is required when mail is set.
	From string
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
	public, ok := lookup("UPMO_PUBLIC_URL")
	if !ok || public == "" {
		return Config{}, fmt.Errorf("config: UPMO_PUBLIC_URL is required")
	}
	parsed, err := url.Parse(public)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return Config{}, fmt.Errorf("config: UPMO_PUBLIC_URL must be an absolute http(s) URL")
	}
	cfg.PublicURL = strings.TrimRight(public, "/")
	cfg.WebAuthnRPID = parsed.Hostname()
	if v, ok := lookup("UPMO_WEBAUTHN_RP_ID"); ok && v != "" {
		cfg.WebAuthnRPID = v
	}
	if v, ok := lookup("UPMO_TRUSTED_PROXIES"); ok && v != "" {
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if !strings.Contains(item, "/") {
				if strings.Contains(item, ":") {
					item += "/128"
				} else {
					item += "/32"
				}
			}
			_, network, err := net.ParseCIDR(item)
			if err != nil {
				return Config{}, fmt.Errorf("config: UPMO_TRUSTED_PROXIES: %w", err)
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, network)
		}
	}
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
	if v, ok := lookup("UPMO_SERVICE_KEY"); ok {
		cfg.ServiceKey = v
	}
	if v, ok := lookup("UPMO_SERVICE_KEY_PREVIOUS"); ok {
		cfg.ServiceKeyPrevious = v
	}
	if v, ok := lookup("UPMO_GATEWAY_URL"); ok && v != "" {
		parsed, err := url.Parse(v)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return Config{}, fmt.Errorf("config: UPMO_GATEWAY_URL must be an absolute http(s) URL")
		}
		cfg.GatewayURL = strings.TrimRight(v, "/")
	}
	smtp, err := loadSMTP(lookup)
	if err != nil {
		return Config{}, err
	}
	cfg.SMTP = smtp
	return cfg, nil
}

// loadSMTP reads UPMO_SMTP_*. None set is mail unset; any set needs a host
// and a sender.
func loadSMTP(lookup func(string) (string, bool)) (SMTP, error) {
	get := func(key string) string {
		v, _ := lookup(key)
		return v
	}
	s := SMTP{
		Host:     get("UPMO_SMTP_HOST"),
		User:     get("UPMO_SMTP_USER"),
		Password: get("UPMO_SMTP_PASSWORD"),
		From:     get("UPMO_SMTP_FROM"),
	}
	port := get("UPMO_SMTP_PORT")
	if s.Host == "" && s.User == "" && s.Password == "" && s.From == "" && port == "" {
		return SMTP{}, nil
	}
	if s.Host == "" {
		return SMTP{}, fmt.Errorf("config: UPMO_SMTP_HOST is required when any UPMO_SMTP_ value is set")
	}
	if strings.ContainsAny(s.Host, " \t\r\n/:") && net.ParseIP(s.Host) == nil {
		return SMTP{}, fmt.Errorf("config: UPMO_SMTP_HOST must be a host name or IP address")
	}
	s.Port = 587
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return SMTP{}, fmt.Errorf("config: UPMO_SMTP_PORT must be a port number")
		}
		s.Port = n
	}
	if (s.User == "") != (s.Password == "") {
		return SMTP{}, fmt.Errorf("config: UPMO_SMTP_USER and UPMO_SMTP_PASSWORD must be set together")
	}
	if s.From == "" {
		return SMTP{}, fmt.Errorf("config: UPMO_SMTP_FROM is required when UPMO_SMTP_HOST is set")
	}
	if _, err := netmail.ParseAddress(s.From); err != nil || strings.ContainsAny(s.From, "\r\n") {
		return SMTP{}, fmt.Errorf("config: UPMO_SMTP_FROM must be an email address")
	}
	return s, nil
}
