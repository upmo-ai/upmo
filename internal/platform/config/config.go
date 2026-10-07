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
	// EmbedRoute is the gateway embed route name (UPMO_EMBED_ROUTE). Empty skips
	// knowledge dense embed wiring in brain.
	EmbedRoute string
	// SandboxdURL is the sandbox process (UPMO_SANDBOXD_URL). Empty until brain calls it.
	SandboxdURL string
	// KnowledgeDir is the Compose volume root for original knowledge file bytes
	// (UPMO_KNOWLEDGE_DIR). Empty refuses new file uploads that need storage.
	KnowledgeDir string
	// SandboxImage is the image a task sandbox starts from (UPMO_SANDBOX_IMAGE).
	// Empty means the runtime default (upmo/sandbox:base).
	SandboxImage string
	// ArtifactsDir is the Compose volume root for artifact bytes (UPMO_ARTIFACTS_DIR).
	// Empty refuses volume downloads until set.
	ArtifactsDir string
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
	if v, ok := lookup("UPMO_EMBED_ROUTE"); ok && strings.TrimSpace(v) != "" {
		cfg.EmbedRoute = strings.TrimSpace(v)
	}
	if v, ok := lookup("UPMO_SANDBOXD_URL"); ok && v != "" {
		parsed, err := url.Parse(v)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return Config{}, fmt.Errorf("config: UPMO_SANDBOXD_URL must be an absolute http(s) URL")
		}
		cfg.SandboxdURL = strings.TrimRight(v, "/")
	}
	if v, ok := lookup("UPMO_SANDBOX_IMAGE"); ok && strings.TrimSpace(v) != "" {
		v = strings.TrimSpace(v)
		if strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return Config{}, fmt.Errorf("config: UPMO_SANDBOX_IMAGE must be one image reference")
		}
		cfg.SandboxImage = v
	}
	if v, ok := lookup("UPMO_ARTIFACTS_DIR"); ok && strings.TrimSpace(v) != "" {
		v = strings.TrimSpace(v)
		if strings.ContainsAny(v, "\x00") || !strings.HasPrefix(v, "/") {
			return Config{}, fmt.Errorf("config: UPMO_ARTIFACTS_DIR must be an absolute path")
		}
		cfg.ArtifactsDir = v
	}
	if v, ok := lookup("UPMO_KNOWLEDGE_DIR"); ok && strings.TrimSpace(v) != "" {
		v = strings.TrimSpace(v)
		if strings.ContainsAny(v, "\x00") || !strings.HasPrefix(v, "/") {
			return Config{}, fmt.Errorf("config: UPMO_KNOWLEDGE_DIR must be an absolute path")
		}
		cfg.KnowledgeDir = v
	}
	smtp, err := loadSMTP(lookup)
	if err != nil {
		return Config{}, err
	}
	cfg.SMTP = smtp
	return cfg, nil
}

// Sandboxd holds the settings sandboxd reads at start. It has no database.
type Sandboxd struct {
	// ListenAddr serves the SandboxProvider port (UPMO_LISTEN_ADDR), :8083 by default.
	ListenAddr string
	// ProxyAddr serves the egress proxy to sandboxes (UPMO_PROXY_ADDR), :3128 by default.
	ProxyAddr string
	// ShutdownTimeout bounds graceful shutdown (UPMO_SHUTDOWN_TIMEOUT).
	ShutdownTimeout time.Duration
	// LogLevel is one of debug, info, warn, error (UPMO_LOG_LEVEL).
	LogLevel string
	// ServiceKey is the master HMAC key (UPMO_SERVICE_KEY). It is required.
	ServiceKey string
	// ServiceKeyPrevious is the previous master key during rotation (UPMO_SERVICE_KEY_PREVIOUS).
	ServiceKeyPrevious string
	// Driver picks the sandbox driver (UPMO_SANDBOX_DRIVER): auto, docker,
	// kubernetes or stub, auto by default. auto uses Docker when the engine answers.
	Driver string
	// EgressProxyURL is the egress proxy as a Docker sandbox reaches it
	// (UPMO_EGRESS_PROXY_URL), http://host.docker.internal:3128 by default.
	// It is http://host[:port] with no user, path or query; the driver adds
	// the sandbox id as the proxy user name.
	EgressProxyURL string
	// SandboxNetwork is the internal Docker network sandboxes join
	// (UPMO_SANDBOX_NETWORK), upmo-sandbox-egress by default.
	SandboxNetwork string
	// EditorImage is the code editor container image (UPMO_EDITOR_IMAGE).
	// Empty means the Docker driver's built-in default.
	EditorImage string
}

// LoadSandboxd reads the sandboxd configuration from the environment.
func LoadSandboxd() (Sandboxd, error) {
	return loadSandboxd(os.LookupEnv)
}

func loadSandboxd(lookup func(string) (string, bool)) (Sandboxd, error) {
	cfg := Sandboxd{
		ListenAddr:      ":8083",
		ProxyAddr:       ":3128",
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        "info",
		Driver:          "auto",
		EgressProxyURL:  "http://host.docker.internal:3128",
		SandboxNetwork:  "upmo-sandbox-egress",
	}
	if v, ok := lookup("UPMO_LISTEN_ADDR"); ok && v != "" {
		cfg.ListenAddr = v
	}
	if v, ok := lookup("UPMO_EGRESS_PROXY_URL"); ok && strings.TrimSpace(v) != "" {
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return Sandboxd{}, fmt.Errorf("config: UPMO_EGRESS_PROXY_URL must be http://host[:port]")
		}
		cfg.EgressProxyURL = u.Scheme + "://" + u.Host
	}
	if v, ok := lookup("UPMO_SANDBOX_NETWORK"); ok && strings.TrimSpace(v) != "" {
		cfg.SandboxNetwork = strings.TrimSpace(v)
	}
	if v, ok := lookup("UPMO_SANDBOX_DRIVER"); ok && v != "" {
		switch v = strings.ToLower(strings.TrimSpace(v)); v {
		case "auto", "docker", "kubernetes", "stub":
			cfg.Driver = v
		default:
			return Sandboxd{}, fmt.Errorf("config: UPMO_SANDBOX_DRIVER must be auto, docker, kubernetes or stub")
		}
	}
	if v, ok := lookup("UPMO_PROXY_ADDR"); ok && v != "" {
		cfg.ProxyAddr = v
	}
	if cfg.ListenAddr == cfg.ProxyAddr {
		return Sandboxd{}, fmt.Errorf("config: UPMO_PROXY_ADDR must differ from UPMO_LISTEN_ADDR")
	}
	if v, ok := lookup("UPMO_SHUTDOWN_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Sandboxd{}, fmt.Errorf("config: UPMO_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	if v, ok := lookup("UPMO_LOG_LEVEL"); ok && v != "" {
		cfg.LogLevel = v
	}
	v, ok := lookup("UPMO_SERVICE_KEY")
	if !ok || v == "" {
		return Sandboxd{}, fmt.Errorf("config: UPMO_SERVICE_KEY is required")
	}
	cfg.ServiceKey = v
	if v, ok := lookup("UPMO_SERVICE_KEY_PREVIOUS"); ok {
		cfg.ServiceKeyPrevious = v
	}
	if v, ok := lookup("UPMO_EDITOR_IMAGE"); ok {
		v = strings.TrimSpace(v)
		if v != "" && strings.ContainsAny(v, " \t\r\n") {
			return Sandboxd{}, fmt.Errorf("config: UPMO_EDITOR_IMAGE must be one image reference")
		}
		cfg.EditorImage = v
	}
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
