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

	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
)

// Config holds the settings brain reads at start.
type Config struct {
	// Env is the deployment environment (UPMO_ENV): production by default,
	// development for the local stack. Production refuses the development
	// keys and passwords compose.yml embeds.
	Env Env
	// DatabaseURL is the Postgres URL (UPMO_DATABASE_URL). It is required.
	DatabaseURL string
	// DatabaseAppURL is the Postgres URL the request pool logs in with, as
	// the upmo_app role (UPMO_DATABASE_APP_URL). It is required for brain;
	// gateway and sandboxd never read it.
	DatabaseAppURL string
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
	// MetricsAddr is the internal HTTP listen address for Prometheus /metrics
	// (UPMO_METRICS_ADDR). Empty disables the metrics listener.
	MetricsAddr string
	// ShutdownTimeout bounds graceful shutdown (UPMO_SHUTDOWN_TIMEOUT).
	ShutdownTimeout time.Duration
	// LogLevel is one of debug, info, warn, error (UPMO_LOG_LEVEL).
	LogLevel string
	// ServiceKey is the master HMAC key for process-to-process calls (UPMO_SERVICE_KEY).
	ServiceKey string
	// ServiceKeyPrevious is the previous master key during rotation (UPMO_SERVICE_KEY_PREVIOUS).
	ServiceKeyPrevious string
	// ProvisionKeys are the upmo_svc_ keys accepted on /api/v1/provision/*
	// (UPMO_PROVISION_KEYS, comma-separated for zero-downtime key rotation).
	// Empty disables all provisioning endpoints (returning 404).
	ProvisionKeys []string
	// GatewayURL is the model gateway (UPMO_GATEWAY_URL). Empty until brain calls it.
	GatewayURL string
	// EmbedRoute is the gateway embed route name (UPMO_EMBED_ROUTE). Empty skips
	// knowledge dense embed wiring in brain.
	EmbedRoute string
	// ChatRoute is the gateway chat route that answers a chat line when the
	// person picks none (UPMO_CHAT_ROUTE). "chat" by default.
	ChatRoute string
	// ChatWorkers is how many chat replies stream at once in one process
	// (UPMO_CHAT_WORKERS). 8 by default.
	ChatWorkers int
	// SandboxdURL is the sandbox process (UPMO_SANDBOXD_URL). Empty until brain calls it.
	SandboxdURL string
	// MarkitdownURL is the conversion sidecar (UPMO_MARKITDOWN_URL). Empty
	// skips the sidecar and passes original text through.
	MarkitdownURL string
	// KnowledgeDir is the Compose volume root for original knowledge file bytes
	// (UPMO_KNOWLEDGE_DIR). Empty refuses new file uploads that need storage.
	KnowledgeDir string
	// SandboxImage is the image a task sandbox starts from (UPMO_SANDBOX_IMAGE).
	// Empty means the runtime default (upmo/sandbox:base).
	SandboxImage string
	// ArtifactsDir is the Compose volume root for artifact bytes (UPMO_ARTIFACTS_DIR).
	// Empty refuses volume downloads until set.
	ArtifactsDir string
	// AuditRetentionDays is how long sealed audit events are kept before their
	// whole expired month is dropped (UPMO_AUDIT_RETENTION_DAYS). 365 by
	// default. Usual presets: 365 (SOC 2 observation period, common ISO 27001
	// practice, PCI DSS), 2190 (six years, HIPAA practice), 2555 (seven
	// years, SOX practice).
	AuditRetentionDays int
	// AuditWebhookURL is the HTTPS webhook sealed audit events are pushed to
	// (UPMO_AUDIT_WEBHOOK_URL). Empty registers no pusher at all.
	AuditWebhookURL string
	// AuditSyslogAddr is the host:port of the syslog collector sealed audit
	// events are pushed to over TLS (UPMO_AUDIT_SYSLOG_ADDR). Empty pushes
	// no syslog.
	AuditSyslogAddr string
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

// Load reads the configuration from the environment and applies defaults,
// then refuses the development keys and passwords unless UPMO_ENV is
// development.
func Load() (Config, error) {
	cfg, err := load(os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	if err := CheckConfig(cfg, false, os.LookupEnv, os.Environ()); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadBrain reads the brain configuration from the environment. Brain alone
// opens a request pool, so UPMO_DATABASE_APP_URL is required for it, and it
// needs UPMO_MASTER_KEY outside development.
func LoadBrain() (Config, error) {
	cfg, err := loadBrain(os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	if err := CheckConfig(cfg, true, os.LookupEnv, os.Environ()); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadBrain(lookup func(string) (string, bool)) (Config, error) {
	cfg, err := load(lookup)
	if err != nil {
		return Config{}, err
	}
	if cfg.DatabaseAppURL == "" {
		return Config{}, fmt.Errorf("config: UPMO_DATABASE_APP_URL is required")
	}
	return cfg, nil
}

func load(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		ListenAddr:         ":8080",
		ShutdownTimeout:    10 * time.Second,
		LogLevel:           "info",
		ChatRoute:          "chat",
		ChatWorkers:        8,
		SessionIdle:        24 * time.Hour,
		SessionMaxAge:      14 * 24 * time.Hour,
		AuditRetentionDays: 365,
	}
	v, ok := lookup("UPMO_DATABASE_URL")
	if !ok || v == "" {
		return Config{}, fmt.Errorf("config: UPMO_DATABASE_URL is required")
	}
	envValue, _ := lookup("UPMO_ENV")
	env, err := ParseEnv(envValue)
	if err != nil {
		return Config{}, err
	}
	cfg.Env = env
	if err := tlscheck.PostgresURL("UPMO_DATABASE_URL", v); err != nil {
		return Config{}, err
	}
	cfg.DatabaseURL = v
	if v, ok := lookup("UPMO_DATABASE_APP_URL"); ok && v != "" {
		if err := tlscheck.PostgresURL("UPMO_DATABASE_APP_URL", v); err != nil {
			return Config{}, err
		}
		cfg.DatabaseAppURL = v
	}
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
	if v, ok := lookup("UPMO_METRICS_ADDR"); ok && v != "" {
		if err := checkMetricsAddr(v); err != nil {
			return Config{}, err
		}
		if v == cfg.ListenAddr {
			return Config{}, fmt.Errorf("config: UPMO_METRICS_ADDR must differ from UPMO_LISTEN_ADDR")
		}
		cfg.MetricsAddr = v
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
	if v, ok := lookup("UPMO_PROVISION_KEYS"); ok && strings.TrimSpace(v) != "" {
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			cfg.ProvisionKeys = append(cfg.ProvisionKeys, item)
		}
	}
	if v, ok := lookup("UPMO_GATEWAY_URL"); ok && v != "" {
		parsed, err := url.Parse(v)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return Config{}, fmt.Errorf("config: UPMO_GATEWAY_URL must be an absolute http(s) URL")
		}
		if err := checkServiceURL("UPMO_GATEWAY_URL", parsed); err != nil {
			return Config{}, err
		}
		cfg.GatewayURL = strings.TrimRight(v, "/")
	}
	if v, ok := lookup("UPMO_EMBED_ROUTE"); ok && strings.TrimSpace(v) != "" {
		cfg.EmbedRoute = strings.TrimSpace(v)
	}
	if v, ok := lookup("UPMO_CHAT_ROUTE"); ok && strings.TrimSpace(v) != "" {
		cfg.ChatRoute = strings.TrimSpace(v)
	}
	if v, ok := lookup("UPMO_CHAT_WORKERS"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("config: UPMO_CHAT_WORKERS must be a positive number")
		}
		cfg.ChatWorkers = n
	}
	if v, ok := lookup("UPMO_AUDIT_RETENTION_DAYS"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 30 || n > 3650 {
			return Config{}, fmt.Errorf("config: UPMO_AUDIT_RETENTION_DAYS must be a number of days between 30 and 3650")
		}
		cfg.AuditRetentionDays = n
	}
	if v, ok := lookup("UPMO_AUDIT_WEBHOOK_URL"); ok && v != "" {
		parsed, err := url.Parse(v)
		if err != nil || parsed.Host == "" || parsed.Scheme != "https" {
			return Config{}, fmt.Errorf("config: UPMO_AUDIT_WEBHOOK_URL must be an absolute https URL")
		}
		cfg.AuditWebhookURL = strings.TrimRight(v, "/")
	}
	if v, ok := lookup("UPMO_AUDIT_SYSLOG_ADDR"); ok && v != "" {
		host, port, err := net.SplitHostPort(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: UPMO_AUDIT_SYSLOG_ADDR must be host:port with a port from 1 to 65535")
		}
		n, err := strconv.Atoi(port)
		if host == "" || err != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("config: UPMO_AUDIT_SYSLOG_ADDR must be host:port with a port from 1 to 65535")
		}
		cfg.AuditSyslogAddr = v
	}
	if v, ok := lookup("UPMO_SANDBOXD_URL"); ok && v != "" {
		parsed, err := url.Parse(v)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return Config{}, fmt.Errorf("config: UPMO_SANDBOXD_URL must be an absolute http(s) URL")
		}
		if err := checkServiceURL("UPMO_SANDBOXD_URL", parsed); err != nil {
			return Config{}, err
		}
		cfg.SandboxdURL = strings.TrimRight(v, "/")
	}
	if v, ok := lookup("UPMO_MARKITDOWN_URL"); ok && v != "" {
		parsed, err := url.Parse(v)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return Config{}, fmt.Errorf("config: UPMO_MARKITDOWN_URL must be an absolute http(s) URL")
		}
		if err := checkServiceURL("UPMO_MARKITDOWN_URL", parsed); err != nil {
			return Config{}, err
		}
		cfg.MarkitdownURL = strings.TrimRight(v, "/")
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
	// Env is the deployment environment (UPMO_ENV): production by default,
	// development for the local stack. Production refuses the development
	// service key compose.yml embeds.
	Env Env
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
	// EditorAddr serves the editor access gate (UPMO_EDITOR_ADDR), :8084 by default.
	EditorAddr string
	// EditorBaseURL is the public http origin for editor URLs (UPMO_EDITOR_BASE_URL).
	EditorBaseURL string
	// MetricsAddr is the internal HTTP listen address for Prometheus /metrics
	// (UPMO_METRICS_ADDR). Empty disables the metrics listener.
	MetricsAddr string
}

// LoadSandboxd reads the sandboxd configuration from the environment, then
// refuses the development service key unless UPMO_ENV is development.
func LoadSandboxd() (Sandboxd, error) {
	cfg, err := loadSandboxd(os.LookupEnv)
	if err != nil {
		return Sandboxd{}, err
	}
	if err := CheckSandboxd(cfg, os.LookupEnv, os.Environ()); err != nil {
		return Sandboxd{}, err
	}
	return cfg, nil
}

func loadSandboxd(lookup func(string) (string, bool)) (Sandboxd, error) {
	cfg := Sandboxd{
		ListenAddr:      ":8083",
		ProxyAddr:       ":3128",
		EditorAddr:      ":8084",
		EditorBaseURL:   "http://127.0.0.1:8084",
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
	if v, ok := lookup("UPMO_EDITOR_ADDR"); ok && v != "" {
		cfg.EditorAddr = v
	}
	if v, ok := lookup("UPMO_EDITOR_BASE_URL"); ok && strings.TrimSpace(v) != "" {
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return Sandboxd{}, fmt.Errorf("config: UPMO_EDITOR_BASE_URL must be http://host[:port]")
		}
		cfg.EditorBaseURL = u.Scheme + "://" + u.Host
	}
	if cfg.ListenAddr == cfg.EditorAddr {
		return Sandboxd{}, fmt.Errorf("config: UPMO_EDITOR_ADDR must differ from UPMO_LISTEN_ADDR")
	}
	if cfg.ProxyAddr == cfg.EditorAddr {
		return Sandboxd{}, fmt.Errorf("config: UPMO_EDITOR_ADDR must differ from UPMO_PROXY_ADDR")
	}
	if v, ok := lookup("UPMO_METRICS_ADDR"); ok && v != "" {
		if err := checkMetricsAddr(v); err != nil {
			return Sandboxd{}, err
		}
		if v == cfg.ListenAddr {
			return Sandboxd{}, fmt.Errorf("config: UPMO_METRICS_ADDR must differ from UPMO_LISTEN_ADDR")
		}
		if v == cfg.ProxyAddr {
			return Sandboxd{}, fmt.Errorf("config: UPMO_METRICS_ADDR must differ from UPMO_PROXY_ADDR")
		}
		if v == cfg.EditorAddr {
			return Sandboxd{}, fmt.Errorf("config: UPMO_METRICS_ADDR must differ from UPMO_EDITOR_ADDR")
		}
		cfg.MetricsAddr = v
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
	envValue, _ := lookup("UPMO_ENV")
	env, err := ParseEnv(envValue)
	if err != nil {
		return Sandboxd{}, err
	}
	cfg.Env = env
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

// checkServiceURL refuses an http service URL whose host is not exempt: the
// internal services may be plain only on loopback or a Compose service name.
func checkServiceURL(name string, u *url.URL) error {
	if u.Scheme == "http" && !tlscheck.ExemptHost(u.Hostname()) {
		return fmt.Errorf("config: %s must be https unless the host is loopback or one DNS label", name)
	}
	return nil
}

func checkMetricsAddr(v string) error {
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return fmt.Errorf("config: UPMO_METRICS_ADDR must be [host]:port with a port from 1 to 65535")
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("config: UPMO_METRICS_ADDR must be [host]:port with a port from 1 to 65535")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("config: UPMO_METRICS_ADDR must be [host]:port with a port from 1 to 65535")
	}
	return nil
}
