// Package config loads process configuration from UPMO_ environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	netmail "net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/editorjwt"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/runjwt"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
)

const (
	// DefaultArtifactsDir is the artifact volume root when unset.
	DefaultArtifactsDir = "/var/upmo/artifacts"
	// DefaultKnowledgeDir is the knowledge volume root when unset.
	DefaultKnowledgeDir = "/var/upmo/knowledge"
	// DefaultAvatarsDir is the avatar volume root when unset.
	DefaultAvatarsDir = "/var/upmo/avatars"
)

// DirFromEnv trims a directory environment value and uses def when it is empty.
func DirFromEnv(name, def string) (string, error) {
	dir := strings.TrimSpace(name)
	if dir == "" {
		dir = def
	}
	if strings.ContainsRune(dir, '\x00') || !filepath.IsAbs(dir) {
		return "", errors.New("must be an absolute path without NUL")
	}
	return dir, nil
}

// Config holds the settings brain reads at start.
type Config struct {
	// Env is the deployment environment (UPMO_ENV): production by default,
	// development for the local stack. Production refuses the development
	// keys and passwords compose.yml embeds.
	Env Env
	// DatabaseURL is the privileged Postgres URL (UPMO_DATABASE_URL): a
	// superuser or BYPASSRLS role that owns the tables. Only the worker and
	// the upmo commands read it; brain, gateway and sandboxd never do.
	DatabaseURL string
	// DatabaseAppURL is the Postgres URL the request pool logs in with, as
	// the upmo_app role (UPMO_DATABASE_APP_URL). It is required for brain;
	// the worker reads it to set the role's password.
	DatabaseAppURL string
	// DatabaseRunnerURL is the Postgres URL brain's task runner logs in with,
	// as the upmo_runner role (UPMO_DATABASE_RUNNER_URL). It is required for
	// brain; the worker reads it to set the role's password.
	DatabaseRunnerURL string
	// DatabaseGatewayURL is the Postgres URL gateway logs in with, as the
	// upmo_gateway role (UPMO_DATABASE_GATEWAY_URL). It is required for
	// gateway; the worker reads it to set the role's password.
	DatabaseGatewayURL string
	// DatabaseSandboxURL is the Postgres URL sandboxd logs in with, as the
	// upmo_sandbox role (UPMO_DATABASE_SANDBOX_URL). The worker reads it to
	// set the role's password.
	DatabaseSandboxURL string
	// WorkerListenAddr is the worker's internal HTTP listener
	// (UPMO_WORKER_LISTEN_ADDR), :8090 by default. It serves provisioning.
	WorkerListenAddr string
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
	// GatewayKey is the upmo_svc_gateway_ key brain signs gateway calls with
	// and gateway verifies them with (UPMO_GATEWAY_KEY). GatewayKeyPrevious
	// is also verified during rotation (UPMO_GATEWAY_KEY_PREVIOUS).
	GatewayKey, GatewayKeyPrevious string
	// SandboxdKey is the upmo_svc_sandboxd_ key brain signs sandboxd calls
	// with (UPMO_SANDBOXD_KEY, UPMO_SANDBOXD_KEY_PREVIOUS).
	SandboxdKey, SandboxdKeyPrevious string
	// MarkitdownKey is the upmo_svc_markitdown_ key brain signs conversion
	// calls with (UPMO_MARKITDOWN_KEY, UPMO_MARKITDOWN_KEY_PREVIOUS).
	MarkitdownKey, MarkitdownKeyPrevious string
	// EditorKey is the upmo_edk_ key brain signs editor JWTs with
	// (UPMO_EDITOR_KEY, UPMO_EDITOR_KEY_PREVIOUS).
	EditorKey, EditorKeyPrevious string
	// RunKey is the upmo_rnk_ key connector run JWTs are signed with
	// (UPMO_RUN_KEY, UPMO_RUN_KEY_PREVIOUS).
	RunKey, RunKeyPrevious string
	// MetricsToken is the upmo_mtr_ scrape bearer GET /metrics requires
	// (UPMO_METRICS_TOKEN). It is required when MetricsAddr is set.
	MetricsToken string
	// WorkerURL is the worker's key service (UPMO_WORKER_URL), for brain and
	// gateway. It must be https outside development.
	WorkerURL string
	// The worker key routes' audience keys, each with a verify-only
	// _PREVIOUS: brain signs with UPMO_WORKER_BRAIN_KEY, gateway with
	// UPMO_WORKER_GATEWAY_KEY, and the worker verifies all three. Each process
	// refuses the ones it does not use.
	WorkerBrainKey, WorkerBrainKeyPrevious       string
	WorkerGatewayKey, WorkerGatewayKeyPrevious   string
	WorkerSandboxdKey, WorkerSandboxdKeyPrevious string
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
	// (UPMO_KNOWLEDGE_DIR). Defaults to DefaultKnowledgeDir.
	KnowledgeDir string
	// SandboxImage is the image a task sandbox starts from (UPMO_SANDBOX_IMAGE).
	// Empty means the runtime default (upmo/sandbox:base).
	SandboxImage string
	// ArtifactsDir is the Compose volume root for artifact bytes (UPMO_ARTIFACTS_DIR).
	// Defaults to DefaultArtifactsDir.
	ArtifactsDir string
	// AvatarsDir is the Compose volume root for user avatar PNG bytes (UPMO_AVATARS_DIR).
	// Defaults to DefaultAvatarsDir.
	AvatarsDir string
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
	// CAFile is the PEM CA file the relay certificate is checked against
	// (UPMO_TLS_CA_FILE). It is set only when mail is set and is not validated
	// here; the send fails closed when production needs it and it is unusable.
	CAFile string
}

// The UPMO_SERVICE_KEY refusals each process gives, naming the keys it reads instead.
const (
	refuseBrain    = "config: UPMO_SERVICE_KEY is not read; set UPMO_GATEWAY_KEY, UPMO_SANDBOXD_KEY, UPMO_MARKITDOWN_KEY, UPMO_EDITOR_KEY and UPMO_RUN_KEY"
	refuseGateway  = "config: UPMO_SERVICE_KEY is not read; set UPMO_GATEWAY_KEY"
	refuseSandboxd = "config: UPMO_SERVICE_KEY is not read; set UPMO_SANDBOXD_KEY and UPMO_EDITOR_KEY"
)

// Which database URLs a process reads.
const (
	// dbOwner is the worker and the upmo commands: the privileged URL, and
	// the serving roles' URLs to set their passwords.
	dbOwner = iota
	// dbBrain is brain HTTP: the app and runner URLs, never the owner URL.
	dbBrain
	// dbGateway is gateway: its own URL, never the owner URL.
	dbGateway
)

// Load reads configuration and requires the master key used by secret-using
// Upmo commands, while development keys and passwords remain allowed locally.
func Load() (Config, error) {
	cfg, err := load(os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	if err := CheckConfig(cfg, true, os.LookupEnv, os.Environ()); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadBrain reads the brain configuration. Brain opens a request pool and
// requires UPMO_DATABASE_APP_URL and UPMO_MASTER_KEY in every environment.
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

// LoadGateway reads the gateway configuration. Gateway verifies calls with
// UPMO_GATEWAY_KEY, so it is required. Gateway never holds a master key: the
// worker unwraps each credential's data key for it.
func LoadGateway() (Config, error) {
	if err := RefuseMasterKey(os.LookupEnv, os.Environ()); err != nil {
		return Config{}, err
	}
	cfg, err := loadGateway(os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	if err := CheckConfig(cfg, false, os.LookupEnv, os.Environ()); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// RefuseMasterKey fails when UPMO_MASTER_KEY or any UPMO_MASTER_KEY_PREVIOUS_
// variable is set at all, for the processes that must never hold one.
func RefuseMasterKey(lookup func(string) (string, bool), environ []string) error {
	if _, ok := lookup("UPMO_MASTER_KEY"); ok {
		return errors.New("config: UPMO_MASTER_KEY is refused")
	}
	for _, entry := range environ {
		if strings.HasPrefix(entry, "UPMO_MASTER_KEY_PREVIOUS_") {
			return errors.New("config: UPMO_MASTER_KEY is refused")
		}
	}
	return nil
}

func loadGateway(lookup func(string) (string, bool)) (Config, error) {
	cfg, err := loadWith(lookup, refuseGateway, false, dbGateway)
	if err != nil {
		return Config{}, err
	}
	if cfg.GatewayKey == "" {
		return Config{}, fmt.Errorf("config: UPMO_GATEWAY_KEY is required")
	}
	return cfg, nil
}

func loadBrain(lookup func(string) (string, bool)) (Config, error) {
	return loadWith(lookup, refuseBrain, true, dbBrain)
}

// load reads the configuration of the worker and the upmo commands.
func load(lookup func(string) (string, bool)) (Config, error) {
	return loadWith(lookup, refuseBrain, true, dbOwner)
}

// loadWith reads the shared configuration and refuses UPMO_SERVICE_KEY with
// the process's own refusal. Only brain reads the keys of other audiences,
// so a gateway never holds them.
func loadWith(lookup func(string) (string, bool), refusal string, brain bool, db int) (Config, error) {
	if err := refuseServiceKey(lookup, refusal); err != nil {
		return Config{}, err
	}
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
	envValue, _ := lookup("UPMO_ENV")
	env, err := ParseEnv(envValue)
	if err != nil {
		return Config{}, err
	}
	cfg.Env = env
	if err := readDatabaseURLs(lookup, &cfg, db); err != nil {
		return Config{}, err
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
	if cfg.GatewayKey, cfg.GatewayKeyPrevious, err = readKeys(lookup, "UPMO_GATEWAY_KEY", audienceKey(svcsig.AudienceGateway)); err != nil {
		return Config{}, err
	}
	if cfg.MetricsToken, err = readMetricsToken(lookup, cfg.MetricsAddr); err != nil {
		return Config{}, err
	}
	if err := readWorkerKeys(lookup, &cfg, db); err != nil {
		return Config{}, err
	}
	if brain {
		if err := readBrainKeys(lookup, &cfg); err != nil {
			return Config{}, err
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
	artDir, _ := lookup("UPMO_ARTIFACTS_DIR")
	cfg.ArtifactsDir, err = DirFromEnv(artDir, DefaultArtifactsDir)
	if err != nil {
		return Config{}, fmt.Errorf("config: UPMO_ARTIFACTS_DIR %w", err)
	}
	knowDir, _ := lookup("UPMO_KNOWLEDGE_DIR")
	cfg.KnowledgeDir, err = DirFromEnv(knowDir, DefaultKnowledgeDir)
	if err != nil {
		return Config{}, fmt.Errorf("config: UPMO_KNOWLEDGE_DIR %w", err)
	}
	avatarDir, _ := lookup("UPMO_AVATARS_DIR")
	cfg.AvatarsDir, err = DirFromEnv(avatarDir, DefaultAvatarsDir)
	if err != nil {
		return Config{}, fmt.Errorf("config: UPMO_AVATARS_DIR %w", err)
	}
	smtp, err := loadSMTP(lookup)
	if err != nil {
		return Config{}, err
	}
	cfg.SMTP = smtp
	return cfg, nil
}

// Sandboxd holds the settings sandboxd reads at start.
type Sandboxd struct {
	// Env is the deployment environment (UPMO_ENV): production by default,
	// development for the local stack. Production refuses the development
	// service key compose.yml embeds.
	Env Env
	// DatabaseSandboxURL is the Postgres URL sandboxd logs in with, as the
	// upmo_sandbox role (UPMO_DATABASE_SANDBOX_URL). Empty opens no database,
	// and every credential read fails closed. Sandboxd never reads
	// UPMO_DATABASE_URL and refuses to start when it is set.
	DatabaseSandboxURL string
	// ListenAddr serves the SandboxProvider port (UPMO_LISTEN_ADDR), :8083 by default.
	ListenAddr string
	// ProxyAddr serves the egress proxy to sandboxes (UPMO_PROXY_ADDR), :3128 by default.
	ProxyAddr string
	// ShutdownTimeout bounds graceful shutdown (UPMO_SHUTDOWN_TIMEOUT).
	ShutdownTimeout time.Duration
	// LogLevel is one of debug, info, warn, error (UPMO_LOG_LEVEL).
	LogLevel string
	// SandboxdKey is the upmo_svc_sandboxd_ key calls are verified with
	// (UPMO_SANDBOXD_KEY). It is required. SandboxdKeyPrevious is also
	// verified during rotation (UPMO_SANDBOXD_KEY_PREVIOUS).
	SandboxdKey, SandboxdKeyPrevious string
	// EditorKey is the upmo_edk_ key editor JWTs are verified with
	// (UPMO_EDITOR_KEY). It is required. EditorKeyPrevious is also verified
	// during rotation (UPMO_EDITOR_KEY_PREVIOUS).
	EditorKey, EditorKeyPrevious string
	// MetricsToken is the upmo_mtr_ scrape bearer GET /metrics requires
	// (UPMO_METRICS_TOKEN). It is required when MetricsAddr is set.
	MetricsToken string
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
	// WorkerURL is the worker's key service (UPMO_WORKER_URL), signed with
	// UPMO_WORKER_SANDBOXD_KEY (and verify-only _PREVIOUS). Both or neither.
	WorkerURL                                    string
	WorkerSandboxdKey, WorkerSandboxdKeyPrevious string
}

// LoadSandboxd reads the sandboxd configuration from the environment, then
// refuses the development service key unless UPMO_ENV is development.
func LoadSandboxd() (Sandboxd, error) {
	if err := RefuseMasterKey(os.LookupEnv, os.Environ()); err != nil {
		return Sandboxd{}, err
	}
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
	if err := refuseServiceKey(lookup, refuseSandboxd); err != nil {
		return Sandboxd{}, err
	}
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
	var err error
	if cfg.SandboxdKey, cfg.SandboxdKeyPrevious, err = readKeys(lookup, "UPMO_SANDBOXD_KEY", audienceKey(svcsig.AudienceSandboxd)); err != nil {
		return Sandboxd{}, err
	}
	if cfg.SandboxdKey == "" {
		return Sandboxd{}, fmt.Errorf("config: UPMO_SANDBOXD_KEY is required")
	}
	if cfg.EditorKey, cfg.EditorKeyPrevious, err = readKeys(lookup, "UPMO_EDITOR_KEY", editorKey); err != nil {
		return Sandboxd{}, err
	}
	if cfg.EditorKey == "" {
		return Sandboxd{}, fmt.Errorf("config: UPMO_EDITOR_KEY is required")
	}
	if cfg.MetricsToken, err = readMetricsToken(lookup, cfg.MetricsAddr); err != nil {
		return Sandboxd{}, err
	}
	envValue, _ := lookup("UPMO_ENV")
	env, err := ParseEnv(envValue)
	if err != nil {
		return Sandboxd{}, err
	}
	cfg.Env = env
	for _, k := range workerKeys[:2] {
		if err := refuseWorkerKey(lookup, k.name, -1); err != nil {
			return Sandboxd{}, err
		}
	}
	if cfg.WorkerSandboxdKey, cfg.WorkerSandboxdKeyPrevious, err = readKeys(lookup, "UPMO_WORKER_SANDBOXD_KEY", audienceKey(svcsig.AudienceWorkerSandboxd)); err != nil {
		return Sandboxd{}, err
	}
	if cfg.WorkerURL, err = readWorkerURL(lookup, env == EnvDevelopment); err != nil {
		return Sandboxd{}, err
	}
	if (cfg.WorkerURL == "") != (cfg.WorkerSandboxdKey == "") {
		return Sandboxd{}, fmt.Errorf("config: UPMO_WORKER_URL and UPMO_WORKER_SANDBOXD_KEY are set together")
	}
	if _, ok := lookup("UPMO_DATABASE_URL"); ok {
		return Sandboxd{}, fmt.Errorf("config: UPMO_DATABASE_URL must not be set for sandboxd; it logs in as upmo_sandbox on UPMO_DATABASE_SANDBOX_URL")
	}
	if v, ok := lookup("UPMO_DATABASE_SANDBOX_URL"); ok && v != "" {
		if err := tlscheck.PostgresURL("UPMO_DATABASE_SANDBOX_URL", v, env == EnvDevelopment); err != nil {
			return Sandboxd{}, err
		}
		cfg.DatabaseSandboxURL = v
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

// readDatabaseURLs reads the database URLs the process holds and checks each
// one's TLS for the environment. Brain and gateway never read
// UPMO_DATABASE_URL, so they cannot open the privileged role.
func readDatabaseURLs(lookup func(string) (string, bool), cfg *Config, db int) error {
	development := cfg.Env == EnvDevelopment
	read := func(name string, required bool, into *string) error {
		v, ok := lookup(name)
		if !ok || v == "" {
			if required {
				return fmt.Errorf("config: %s is required", name)
			}
			return nil
		}
		if err := tlscheck.PostgresURL(name, v, development); err != nil {
			return err
		}
		*into = v
		return nil
	}
	switch db {
	case dbBrain:
		if err := read("UPMO_DATABASE_APP_URL", true, &cfg.DatabaseAppURL); err != nil {
			return err
		}
		return read("UPMO_DATABASE_RUNNER_URL", true, &cfg.DatabaseRunnerURL)
	case dbGateway:
		return read("UPMO_DATABASE_GATEWAY_URL", true, &cfg.DatabaseGatewayURL)
	}
	for _, s := range []struct {
		name     string
		required bool
		into     *string
	}{
		{"UPMO_DATABASE_URL", true, &cfg.DatabaseURL},
		{"UPMO_DATABASE_APP_URL", false, &cfg.DatabaseAppURL},
		{"UPMO_DATABASE_RUNNER_URL", false, &cfg.DatabaseRunnerURL},
		{"UPMO_DATABASE_GATEWAY_URL", false, &cfg.DatabaseGatewayURL},
		{"UPMO_DATABASE_SANDBOX_URL", false, &cfg.DatabaseSandboxURL},
	} {
		if err := read(s.name, s.required, s.into); err != nil {
			return err
		}
	}
	cfg.WorkerListenAddr = ":8090"
	if v, ok := lookup("UPMO_WORKER_LISTEN_ADDR"); ok && v != "" {
		cfg.WorkerListenAddr = v
	}
	return nil
}

// The worker key variables, and which process signs with each.
var workerKeys = []struct {
	name, audience string
	db             int
}{
	{"UPMO_WORKER_BRAIN_KEY", svcsig.AudienceWorkerBrain, dbBrain},
	{"UPMO_WORKER_GATEWAY_KEY", svcsig.AudienceWorkerGateway, dbGateway},
	{"UPMO_WORKER_SANDBOXD_KEY", svcsig.AudienceWorkerSandboxd, -1},
}

// readWorkerKeys reads the worker keys for one process. The worker and the
// upmo commands read all three. Brain and gateway read their own, refuse the
// others even when empty, and require UPMO_WORKER_URL and their own key.
func readWorkerKeys(lookup func(string) (string, bool), cfg *Config, db int) error {
	into := map[string][2]*string{
		"UPMO_WORKER_BRAIN_KEY":    {&cfg.WorkerBrainKey, &cfg.WorkerBrainKeyPrevious},
		"UPMO_WORKER_GATEWAY_KEY":  {&cfg.WorkerGatewayKey, &cfg.WorkerGatewayKeyPrevious},
		"UPMO_WORKER_SANDBOXD_KEY": {&cfg.WorkerSandboxdKey, &cfg.WorkerSandboxdKeyPrevious},
	}
	var own string
	for _, k := range workerKeys {
		if db != dbOwner && k.db != db {
			if err := refuseWorkerKey(lookup, k.name, db); err != nil {
				return err
			}
			continue
		}
		cur, prev, err := readKeys(lookup, k.name, audienceKey(k.audience))
		if err != nil {
			return err
		}
		*into[k.name][0], *into[k.name][1] = cur, prev
		own = k.name
	}
	if db == dbOwner {
		return nil
	}
	workerURL, err := readWorkerURL(lookup, cfg.Env == EnvDevelopment)
	if err != nil {
		return err
	}
	// Brain seals and gateway opens every saved provider key through the
	// worker, in every environment.
	if workerURL == "" {
		return errors.New("config: UPMO_WORKER_URL is required")
	}
	if *into[own][0] == "" {
		return fmt.Errorf("config: %s is required when UPMO_WORKER_URL is set", own)
	}
	cfg.WorkerURL = workerURL
	return nil
}

// refuseWorkerKey fails when a worker key for another process is set at all.
func refuseWorkerKey(lookup func(string) (string, bool), name string, db int) error {
	process := map[int]string{dbBrain: "brain", dbGateway: "gateway"}[db]
	if process == "" {
		process = "sandboxd"
	}
	for _, n := range []string{name, name + "_PREVIOUS"} {
		if _, ok := lookup(n); ok {
			return fmt.Errorf("config: %s must not be set for %s", n, process)
		}
	}
	return nil
}

// readWorkerURL reads UPMO_WORKER_URL. Outside development it must be https;
// in development http is allowed on loopback and on one DNS label.
func readWorkerURL(lookup func(string) (string, bool), development bool) (string, error) {
	v, ok := lookup("UPMO_WORKER_URL")
	if !ok || strings.TrimSpace(v) == "" {
		return "", nil
	}
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("config: UPMO_WORKER_URL must be an absolute https URL with no path")
	}
	if u.Scheme == "http" && !tlscheck.PlaintextAllowed(development, u.Hostname()) {
		return "", fmt.Errorf("config: UPMO_WORKER_URL must be https")
	}
	return u.Scheme + "://" + u.Host, nil
}

// refuseServiceKey fails when the retired service master is set at all,
// even empty, so a deployment cannot keep it next to the per-audience keys.
func refuseServiceKey(lookup func(string) (string, bool), refusal string) error {
	for _, name := range []string{"UPMO_SERVICE_KEY", "UPMO_SERVICE_KEY_PREVIOUS"} {
		if _, ok := lookup(name); ok {
			return errors.New(refusal)
		}
	}
	return nil
}

// readBrainKeys reads the keys only brain holds: the sandboxd, markitdown,
// editor, run and provision keys.
func readBrainKeys(lookup func(string) (string, bool), cfg *Config) error {
	var err error
	if cfg.SandboxdKey, cfg.SandboxdKeyPrevious, err = readKeys(lookup, "UPMO_SANDBOXD_KEY", audienceKey(svcsig.AudienceSandboxd)); err != nil {
		return err
	}
	if cfg.MarkitdownKey, cfg.MarkitdownKeyPrevious, err = readKeys(lookup, "UPMO_MARKITDOWN_KEY", audienceKey(svcsig.AudienceMarkitdown)); err != nil {
		return err
	}
	if cfg.EditorKey, cfg.EditorKeyPrevious, err = readKeys(lookup, "UPMO_EDITOR_KEY", editorKey); err != nil {
		return err
	}
	if cfg.RunKey, cfg.RunKeyPrevious, err = readKeys(lookup, "UPMO_RUN_KEY", runKey); err != nil {
		return err
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
	return nil
}

func audienceKey(audience string) func(string) error {
	return func(v string) error {
		_, err := svcsig.ParseAudienceKey(v, audience)
		return err
	}
}

func editorKey(v string) error {
	_, err := editorjwt.ParseKey(v)
	return err
}

func runKey(v string) error {
	_, err := runjwt.ParseKey(v)
	return err
}

// readKeys reads name and name_PREVIOUS, both optional here, and checks
// each set value with parse. A previous key needs a current one to sign with.
// Errors name the variable, never the value.
func readKeys(lookup func(string) (string, bool), name string, parse func(string) error) (current, previous string, err error) {
	current, _ = lookup(name)
	previous, _ = lookup(name + "_PREVIOUS")
	if current == "" && previous != "" {
		return "", "", fmt.Errorf("config: %s_PREVIOUS is set without %s", name, name)
	}
	for _, s := range []Setting{{Name: name, Value: current}, {Name: name + "_PREVIOUS", Value: previous}} {
		if s.Value == "" {
			continue
		}
		if err := parse(s.Value); err != nil {
			return "", "", fmt.Errorf("config: %s: %w", s.Name, err)
		}
	}
	return current, previous, nil
}

// readMetricsToken reads UPMO_METRICS_TOKEN, required whenever a metrics
// listener is configured.
func readMetricsToken(lookup func(string) (string, bool), metricsAddr string) (string, error) {
	v, _ := lookup("UPMO_METRICS_TOKEN")
	if v == "" {
		if metricsAddr != "" {
			return "", fmt.Errorf("config: UPMO_METRICS_TOKEN is required when UPMO_METRICS_ADDR is set")
		}
		return "", nil
	}
	if err := metrics.ParseToken(v); err != nil {
		return "", fmt.Errorf("config: UPMO_METRICS_TOKEN: %w", err)
	}
	return v, nil
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
	s.CAFile = strings.TrimSpace(get("UPMO_TLS_CA_FILE"))
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
