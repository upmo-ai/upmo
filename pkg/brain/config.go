package brain

import (
	"net"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/config"
)

// Config holds the settings brain reads at startup.
type Config struct {
	Env                   string
	DatabaseAppURL        string
	DatabaseRunnerURL     string
	SessionIdle           time.Duration
	SessionMaxAge         time.Duration
	PublicURL             string
	WebAuthnRPID          string
	TrustedProxies        []*net.IPNet
	ListenAddr            string
	MetricsAddr           string
	ShutdownTimeout       time.Duration
	LogLevel              string
	GatewayKey            string
	GatewayKeyPrevious    string
	SandboxdKey           string
	SandboxdKeyPrevious   string
	MarkitdownKey         string
	MarkitdownKeyPrevious string
	EditorKey             string
	EditorKeyPrevious     string
	RunKey                string
	RunKeyPrevious        string
	MetricsToken          string
	ProvisionKeys         []string
	GatewayURL            string
	EmbedRoute            string
	ChatRoute             string
	ChatWorkers           int
	SandboxdURL           string
	MarkitdownURL         string
	KnowledgeDir          string
	SandboxImage          string
	ArtifactsDir          string
	AvatarsDir            string
	AuditRetentionDays    int
	AuditWebhookURL       string
	AuditSyslogAddr       string
	SMTP                  SMTPConfig
}

// SMTPConfig holds the mail relay settings for invite and recover emails.
type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	CAFile   string
}

// LoadConfig reads the brain configuration from UPMO_ environment variables.
func LoadConfig() (Config, error) {
	cfg, err := config.LoadBrain()
	if err != nil {
		return Config{}, err
	}
	return fromInternalConfig(cfg), nil
}

func fromInternalConfig(cfg config.Config) Config {
	return Config{
		Env:                   string(cfg.Env),
		DatabaseAppURL:        cfg.DatabaseAppURL,
		DatabaseRunnerURL:     cfg.DatabaseRunnerURL,
		SessionIdle:           cfg.SessionIdle,
		SessionMaxAge:         cfg.SessionMaxAge,
		PublicURL:             cfg.PublicURL,
		WebAuthnRPID:          cfg.WebAuthnRPID,
		TrustedProxies:        cfg.TrustedProxies,
		ListenAddr:            cfg.ListenAddr,
		MetricsAddr:           cfg.MetricsAddr,
		ShutdownTimeout:       cfg.ShutdownTimeout,
		LogLevel:              cfg.LogLevel,
		GatewayKey:            cfg.GatewayKey,
		GatewayKeyPrevious:    cfg.GatewayKeyPrevious,
		SandboxdKey:           cfg.SandboxdKey,
		SandboxdKeyPrevious:   cfg.SandboxdKeyPrevious,
		MarkitdownKey:         cfg.MarkitdownKey,
		MarkitdownKeyPrevious: cfg.MarkitdownKeyPrevious,
		EditorKey:             cfg.EditorKey,
		EditorKeyPrevious:     cfg.EditorKeyPrevious,
		RunKey:                cfg.RunKey,
		RunKeyPrevious:        cfg.RunKeyPrevious,
		MetricsToken:          cfg.MetricsToken,
		ProvisionKeys:         cfg.ProvisionKeys,
		GatewayURL:            cfg.GatewayURL,
		EmbedRoute:            cfg.EmbedRoute,
		ChatRoute:             cfg.ChatRoute,
		ChatWorkers:           cfg.ChatWorkers,
		SandboxdURL:           cfg.SandboxdURL,
		MarkitdownURL:         cfg.MarkitdownURL,
		KnowledgeDir:          cfg.KnowledgeDir,
		SandboxImage:          cfg.SandboxImage,
		ArtifactsDir:          cfg.ArtifactsDir,
		AvatarsDir:            cfg.AvatarsDir,
		AuditRetentionDays:    cfg.AuditRetentionDays,
		AuditWebhookURL:       cfg.AuditWebhookURL,
		AuditSyslogAddr:       cfg.AuditSyslogAddr,
		SMTP: SMTPConfig{
			Host:     cfg.SMTP.Host,
			Port:     cfg.SMTP.Port,
			User:     cfg.SMTP.User,
			Password: cfg.SMTP.Password,
			From:     cfg.SMTP.From,
			CAFile:   cfg.SMTP.CAFile,
		},
	}
}

func toInternalConfig(cfg Config) config.Config {
	return config.Config{
		Env:                   config.Env(cfg.Env),
		DatabaseAppURL:        cfg.DatabaseAppURL,
		DatabaseRunnerURL:     cfg.DatabaseRunnerURL,
		SessionIdle:           cfg.SessionIdle,
		SessionMaxAge:         cfg.SessionMaxAge,
		PublicURL:             cfg.PublicURL,
		WebAuthnRPID:          cfg.WebAuthnRPID,
		TrustedProxies:        cfg.TrustedProxies,
		ListenAddr:            cfg.ListenAddr,
		MetricsAddr:           cfg.MetricsAddr,
		ShutdownTimeout:       cfg.ShutdownTimeout,
		LogLevel:              cfg.LogLevel,
		GatewayKey:            cfg.GatewayKey,
		GatewayKeyPrevious:    cfg.GatewayKeyPrevious,
		SandboxdKey:           cfg.SandboxdKey,
		SandboxdKeyPrevious:   cfg.SandboxdKeyPrevious,
		MarkitdownKey:         cfg.MarkitdownKey,
		MarkitdownKeyPrevious: cfg.MarkitdownKeyPrevious,
		EditorKey:             cfg.EditorKey,
		EditorKeyPrevious:     cfg.EditorKeyPrevious,
		RunKey:                cfg.RunKey,
		RunKeyPrevious:        cfg.RunKeyPrevious,
		MetricsToken:          cfg.MetricsToken,
		ProvisionKeys:         cfg.ProvisionKeys,
		GatewayURL:            cfg.GatewayURL,
		EmbedRoute:            cfg.EmbedRoute,
		ChatRoute:             cfg.ChatRoute,
		ChatWorkers:           cfg.ChatWorkers,
		SandboxdURL:           cfg.SandboxdURL,
		MarkitdownURL:         cfg.MarkitdownURL,
		KnowledgeDir:          cfg.KnowledgeDir,
		SandboxImage:          cfg.SandboxImage,
		ArtifactsDir:          cfg.ArtifactsDir,
		AvatarsDir:            cfg.AvatarsDir,
		AuditRetentionDays:    cfg.AuditRetentionDays,
		AuditWebhookURL:       cfg.AuditWebhookURL,
		AuditSyslogAddr:       cfg.AuditSyslogAddr,
		SMTP: config.SMTP{
			Host:     cfg.SMTP.Host,
			Port:     cfg.SMTP.Port,
			User:     cfg.SMTP.User,
			Password: cfg.SMTP.Password,
			From:     cfg.SMTP.From,
			CAFile:   cfg.SMTP.CAFile,
		},
	}
}
