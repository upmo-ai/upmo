package config

import (
	"strings"
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
	if cfg.GatewayURL != "" {
		t.Errorf("GatewayURL = %q", cfg.GatewayURL)
	}
}

func TestLoadGatewayURL(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
		"UPMO_GATEWAY_URL":  "http://gateway:8080/",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GatewayURL != "http://gateway:8080" {
		t.Fatalf("GatewayURL = %q", cfg.GatewayURL)
	}
	if _, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
		"UPMO_GATEWAY_URL":  "gateway:8080",
	})); err == nil {
		t.Fatal("relative gateway url")
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

func TestLoadSMTPUnset(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
		"UPMO_SMTP_HOST":    "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP != (SMTP{}) {
		t.Fatalf("SMTP = %+v", cfg.SMTP)
	}
}

func TestLoadSMTP(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL":  "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":    "http://localhost:5173",
		"UPMO_SMTP_HOST":     "smtp.example.com",
		"UPMO_SMTP_USER":     "relay",
		"UPMO_SMTP_PASSWORD": "relay-password",
		"UPMO_SMTP_FROM":     "Upmo <noreply@example.com>",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := SMTP{Host: "smtp.example.com", Port: 587, User: "relay", Password: "relay-password", From: "Upmo <noreply@example.com>"}
	if cfg.SMTP != want {
		t.Fatalf("SMTP = %+v", cfg.SMTP)
	}
	cfg, err = load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
		"UPMO_SMTP_HOST":    "mailpit",
		"UPMO_SMTP_PORT":    "1025",
		"UPMO_SMTP_FROM":    "noreply@example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Port != 1025 || cfg.SMTP.User != "" {
		t.Fatalf("SMTP = %+v", cfg.SMTP)
	}
}

func TestLoadSMTPPartialOrInvalid(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"from without host", map[string]string{"UPMO_SMTP_FROM": "noreply@example.com"}, "UPMO_SMTP_HOST is required"},
		{"password without host", map[string]string{"UPMO_SMTP_PASSWORD": "x"}, "UPMO_SMTP_HOST is required"},
		{"host without from", map[string]string{"UPMO_SMTP_HOST": "smtp.example.com"}, "UPMO_SMTP_FROM is required"},
		{"bad from", map[string]string{"UPMO_SMTP_HOST": "smtp.example.com", "UPMO_SMTP_FROM": "not an address"}, "UPMO_SMTP_FROM must be an email address"},
		{"bad port", map[string]string{"UPMO_SMTP_HOST": "smtp.example.com", "UPMO_SMTP_FROM": "a@example.com", "UPMO_SMTP_PORT": "smtp"}, "UPMO_SMTP_PORT must be a port number"},
		{"port out of range", map[string]string{"UPMO_SMTP_HOST": "smtp.example.com", "UPMO_SMTP_FROM": "a@example.com", "UPMO_SMTP_PORT": "70000"}, "UPMO_SMTP_PORT must be a port number"},
		{"user without password", map[string]string{"UPMO_SMTP_HOST": "smtp.example.com", "UPMO_SMTP_FROM": "a@example.com", "UPMO_SMTP_USER": "relay"}, "must be set together"},
		{"host with port", map[string]string{"UPMO_SMTP_HOST": "smtp.example.com:587", "UPMO_SMTP_FROM": "a@example.com"}, "UPMO_SMTP_HOST must be a host name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
				"UPMO_PUBLIC_URL":   "http://localhost:5173",
			}
			for k, v := range tc.env {
				env[k] = v
			}
			_, err := load(lookupFrom(env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "relay-password") {
				t.Fatalf("err holds the password: %v", err)
			}
		})
	}
}

func TestLoadSandboxdURL(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SandboxdURL != "" {
		t.Fatalf("SandboxdURL = %q, want empty", cfg.SandboxdURL)
	}
	base["UPMO_SANDBOXD_URL"] = "http://sandboxd:8083/"
	cfg, err = load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SandboxdURL != "http://sandboxd:8083" {
		t.Fatalf("SandboxdURL = %q", cfg.SandboxdURL)
	}
	base["UPMO_SANDBOXD_URL"] = "sandboxd:8083"
	if _, err := load(lookupFrom(base)); err == nil {
		t.Fatal("relative sandboxd url")
	}
}

func TestLoadSeatImage(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil || cfg.SeatImage != "" {
		t.Fatalf("default = %q %v", cfg.SeatImage, err)
	}
	base["UPMO_SEAT_IMAGE"] = " upmo/sandbox:base "
	cfg, err = load(lookupFrom(base))
	if err != nil || cfg.SeatImage != "upmo/sandbox:base" {
		t.Fatalf("SeatImage = %q %v", cfg.SeatImage, err)
	}
	base["UPMO_SEAT_IMAGE"] = "upmo/sandbox:base --privileged"
	if _, err := load(lookupFrom(base)); err == nil {
		t.Fatal("seat image with a space")
	}
}

func TestLoadSandboxd(t *testing.T) {
	cfg, err := loadSandboxd(lookupFrom(map[string]string{"UPMO_SERVICE_KEY": "upmo_svc_x"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8083" || cfg.ProxyAddr != ":3128" || cfg.ShutdownTimeout != 10*time.Second || cfg.LogLevel != "info" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.ServiceKey != "upmo_svc_x" || cfg.ServiceKeyPrevious != "" {
		t.Fatalf("service keys = %q %q", cfg.ServiceKey, cfg.ServiceKeyPrevious)
	}
	if cfg.Driver != "auto" {
		t.Fatalf("Driver = %q, want auto", cfg.Driver)
	}
	if cfg.EgressProxyURL != "http://host.docker.internal:3128" || cfg.SandboxNetwork != "upmo-sandbox-egress" {
		t.Fatalf("egress defaults = %q %q", cfg.EgressProxyURL, cfg.SandboxNetwork)
	}

	cfg, err = loadSandboxd(lookupFrom(map[string]string{
		"UPMO_SERVICE_KEY":          "upmo_svc_x",
		"UPMO_SERVICE_KEY_PREVIOUS": "upmo_svc_y",
		"UPMO_LISTEN_ADDR":          ":9000",
		"UPMO_PROXY_ADDR":           ":9001",
		"UPMO_SHUTDOWN_TIMEOUT":     "3s",
		"UPMO_LOG_LEVEL":            "debug",
		"UPMO_SANDBOX_DRIVER":       " Docker ",
		"UPMO_EGRESS_PROXY_URL":     " http://sandboxd:3128/ ",
		"UPMO_SANDBOX_NETWORK":      " upm114_sandbox-egress ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9000" || cfg.ProxyAddr != ":9001" || cfg.ShutdownTimeout != 3*time.Second || cfg.LogLevel != "debug" || cfg.ServiceKeyPrevious != "upmo_svc_y" || cfg.Driver != "docker" {
		t.Fatalf("overrides = %+v", cfg)
	}
	if cfg.EgressProxyURL != "http://sandboxd:3128" || cfg.SandboxNetwork != "upm114_sandbox-egress" {
		t.Fatalf("egress overrides = %q %q", cfg.EgressProxyURL, cfg.SandboxNetwork)
	}

	for name, env := range map[string]map[string]string{
		"no key":       {},
		"empty key":    {"UPMO_SERVICE_KEY": ""},
		"same address": {"UPMO_SERVICE_KEY": "upmo_svc_x", "UPMO_LISTEN_ADDR": ":9000", "UPMO_PROXY_ADDR": ":9000"},
		"bad timeout":  {"UPMO_SERVICE_KEY": "upmo_svc_x", "UPMO_SHUTDOWN_TIMEOUT": "soon"},
		"bad driver":   {"UPMO_SERVICE_KEY": "upmo_svc_x", "UPMO_SANDBOX_DRIVER": "nomad"},
		"https proxy":  {"UPMO_SERVICE_KEY": "upmo_svc_x", "UPMO_EGRESS_PROXY_URL": "https://sandboxd:3128"},
		"proxy user":   {"UPMO_SERVICE_KEY": "upmo_svc_x", "UPMO_EGRESS_PROXY_URL": "http://box@sandboxd:3128"},
		"proxy path":   {"UPMO_SERVICE_KEY": "upmo_svc_x", "UPMO_EGRESS_PROXY_URL": "http://sandboxd:3128/x"},
		"proxy host":   {"UPMO_SERVICE_KEY": "upmo_svc_x", "UPMO_EGRESS_PROXY_URL": "sandboxd:3128"},
	} {
		if _, err := loadSandboxd(lookupFrom(env)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestLoadArtifactsDir(t *testing.T) {
	env := map[string]string{
		"UPMO_DATABASE_URL":  "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":    "http://localhost:5173",
		"UPMO_ARTIFACTS_DIR": "/var/upmo/artifacts",
	}
	cfg, err := load(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ArtifactsDir != "/var/upmo/artifacts" {
		t.Fatalf("dir = %q", cfg.ArtifactsDir)
	}
}

func TestLoadArtifactsDirMustBeAbsolute(t *testing.T) {
	env := map[string]string{
		"UPMO_DATABASE_URL":  "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":    "http://localhost:5173",
		"UPMO_ARTIFACTS_DIR": "relative/path",
	}
	_, err := load(lookupFrom(env))
	if err == nil || !strings.Contains(err.Error(), "UPMO_ARTIFACTS_DIR") {
		t.Fatalf("error = %v", err)
	}
}
