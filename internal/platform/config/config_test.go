package config

import (
	"reflect"
	"strconv"
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
	if cfg.GatewayKey != "" || cfg.SandboxdKey != "" || cfg.MarkitdownKey != "" || cfg.EditorKey != "" || cfg.RunKey != "" || cfg.MetricsToken != "" {
		t.Errorf("keys = %+v", cfg)
	}
	if cfg.GatewayURL != "" {
		t.Errorf("GatewayURL = %q", cfg.GatewayURL)
	}
	if cfg.MetricsAddr != "" {
		t.Errorf("MetricsAddr = %q, want empty", cfg.MetricsAddr)
	}
	if cfg.ArtifactsDir != "/var/upmo/artifacts" || cfg.KnowledgeDir != "/var/upmo/knowledge" || cfg.AvatarsDir != "/var/upmo/avatars" {
		t.Fatalf("volume defaults = %q %q %q", cfg.ArtifactsDir, cfg.KnowledgeDir, cfg.AvatarsDir)
	}
}

func TestDirFromEnv(t *testing.T) {
	def := t.TempDir()
	for _, tc := range []struct {
		value string
		want  string
	}{
		{"", def},
		{" \t\n", def},
		{" /tmp/upmo-custom ", "/tmp/upmo-custom"},
	} {
		got, err := DirFromEnv(tc.value, def)
		if err != nil || got != tc.want {
			t.Errorf("DirFromEnv(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
		}
	}
	for _, value := range []string{"relative/path", "/tmp/bad\x00path"} {
		if _, err := DirFromEnv(value, def); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestLoadVolumeDirs(t *testing.T) {
	for _, name := range []string{"UPMO_ARTIFACTS_DIR", "UPMO_KNOWLEDGE_DIR", "UPMO_AVATARS_DIR"} {
		for _, value := range []string{"", " \t", " /tmp/custom-volume ", "relative", "/tmp/bad\x00path"} {
			env := map[string]string{
				"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
				"UPMO_PUBLIC_URL":   "http://localhost:5173",
				name:                value,
			}
			cfg, err := load(lookupFrom(env))
			if value == "relative" || strings.ContainsRune(value, '\x00') {
				if err == nil || !strings.Contains(err.Error(), name) {
					t.Errorf("%s=%q: error = %v", name, value, err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			wantArt, wantKnow, wantAvatars := "/var/upmo/artifacts", "/var/upmo/knowledge", "/var/upmo/avatars"
			if strings.TrimSpace(value) != "" {
				switch name {
				case "UPMO_ARTIFACTS_DIR":
					wantArt = "/tmp/custom-volume"
				case "UPMO_KNOWLEDGE_DIR":
					wantKnow = "/tmp/custom-volume"
				case "UPMO_AVATARS_DIR":
					wantAvatars = "/tmp/custom-volume"
				}
			}
			if cfg.ArtifactsDir != wantArt || cfg.KnowledgeDir != wantKnow || cfg.AvatarsDir != wantAvatars {
				t.Errorf("%s=%q: dirs = %q %q %q", name, value, cfg.ArtifactsDir, cfg.KnowledgeDir, cfg.AvatarsDir)
			}
		}
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

func TestLoadEmbedRoute(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
		"UPMO_EMBED_ROUTE":  " knowledge-embed ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbedRoute != "knowledge-embed" {
		t.Fatalf("EmbedRoute = %q", cfg.EmbedRoute)
	}
}

func TestLoadChatRouteAndWorkers(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChatRoute != "chat" || cfg.ChatWorkers != 8 {
		t.Fatalf("defaults = %q %d", cfg.ChatRoute, cfg.ChatWorkers)
	}
	base["UPMO_CHAT_ROUTE"] = " draft "
	base["UPMO_CHAT_WORKERS"] = "3"
	cfg, err = load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChatRoute != "draft" || cfg.ChatWorkers != 3 {
		t.Fatalf("set = %q %d", cfg.ChatRoute, cfg.ChatWorkers)
	}
	base["UPMO_CHAT_WORKERS"] = "0"
	if _, err := load(lookupFrom(base)); err == nil {
		t.Fatal("zero workers")
	}
}

func TestLoadKnowledgeDir(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL":  "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":    "http://localhost:5173",
		"UPMO_KNOWLEDGE_DIR": "/var/upmo/knowledge",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KnowledgeDir != "/var/upmo/knowledge" {
		t.Fatalf("KnowledgeDir = %q", cfg.KnowledgeDir)
	}
	if _, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL":  "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":    "http://localhost:5173",
		"UPMO_KNOWLEDGE_DIR": "relative/path",
	})); err == nil {
		t.Fatal("relative knowledge dir")
	}
}

// Test keys: base64 of 32 non-uniform bytes, one per variable.
const (
	gwKey    = "upmo_svc_gateway_Z2F0ZXdheS1rZXktZm9yLXRoZS1jb25maWctdGVzdCE"
	gwPrev   = "upmo_svc_gateway_cHJldmlvdXMtZ2F0ZXdheS1rZXktY29uZmlnLXRlc3Q"
	sbxKey   = "upmo_svc_sandboxd_c2FuZGJveGQta2V5LWZvci10aGUtY29uZmlnLXRlc3Q"
	sbxPrev  = "upmo_svc_sandboxd_cHJldmlvdXMtc2FuZGJveGQta2V5LWNvbmZpZy10c3Q"
	mdKey    = "upmo_svc_markitdown_bWFya2l0ZG93bi1rZXktZm9yLWNvbmZpZy10ZXN0cyE"
	edKey    = "upmo_edk_ZWRpdG9yLWtleS1mb3ItdGhlLWNvbmZpZy10ZXN0ISE"
	edPrev   = "upmo_edk_cHJldmlvdXMtZWRpdG9yLWtleS1jb25maWctdGVzdCE"
	rnkKey   = "upmo_rnk_cnVuLWtleS1mb3ItdGhlLWNvbmZpZy10ZXN0ISEhISE"
	mtrToken = "upmo_mtr_bWV0cmljcy10b2tlbi1mb3ItdGhlLWNvbmZpZy10c3Q" // #nosec G101 -- fixed test token, not a credential
)

func TestLoadKeys(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL":          "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":            "http://localhost:5173",
		"UPMO_GATEWAY_KEY":           gwKey,
		"UPMO_GATEWAY_KEY_PREVIOUS":  gwPrev,
		"UPMO_SANDBOXD_KEY":          sbxKey,
		"UPMO_SANDBOXD_KEY_PREVIOUS": sbxPrev,
		"UPMO_MARKITDOWN_KEY":        mdKey,
		"UPMO_EDITOR_KEY":            edKey,
		"UPMO_EDITOR_KEY_PREVIOUS":   edPrev,
		"UPMO_RUN_KEY":               rnkKey,
		"UPMO_METRICS_ADDR":          "127.0.0.1:9090",
		"UPMO_METRICS_TOKEN":         mtrToken,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GatewayKey != gwKey || cfg.GatewayKeyPrevious != gwPrev || cfg.SandboxdKey != sbxKey || cfg.SandboxdKeyPrevious != sbxPrev ||
		cfg.MarkitdownKey != mdKey || cfg.MarkitdownKeyPrevious != "" || cfg.EditorKey != edKey || cfg.EditorKeyPrevious != edPrev ||
		cfg.RunKey != rnkKey || cfg.RunKeyPrevious != "" || cfg.MetricsToken != mtrToken {
		t.Fatalf("keys = %+v", cfg)
	}
}

func TestLoadRefusesWrongKeys(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"service master", map[string]string{"UPMO_SERVICE_KEY": "upmo_svc_x"}, "config: UPMO_SERVICE_KEY is not read; set UPMO_GATEWAY_KEY, UPMO_SANDBOXD_KEY, UPMO_MARKITDOWN_KEY, UPMO_EDITOR_KEY and UPMO_RUN_KEY"},
		{"empty service master", map[string]string{"UPMO_SERVICE_KEY": ""}, "config: UPMO_SERVICE_KEY is not read; set UPMO_GATEWAY_KEY, UPMO_SANDBOXD_KEY, UPMO_MARKITDOWN_KEY, UPMO_EDITOR_KEY and UPMO_RUN_KEY"},
		{"previous service master", map[string]string{"UPMO_SERVICE_KEY_PREVIOUS": "upmo_svc_x"}, "config: UPMO_SERVICE_KEY is not read; set UPMO_GATEWAY_KEY, UPMO_SANDBOXD_KEY, UPMO_MARKITDOWN_KEY, UPMO_EDITOR_KEY and UPMO_RUN_KEY"},
		{"sandboxd key as gateway key", map[string]string{"UPMO_GATEWAY_KEY": sbxKey}, "config: UPMO_GATEWAY_KEY: svcsig: key is not a gateway key"},
		{"gateway key as previous sandboxd key", map[string]string{"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_SANDBOXD_KEY_PREVIOUS": gwKey}, "config: UPMO_SANDBOXD_KEY_PREVIOUS: svcsig: key is not a sandboxd key"},
		{"raw master as gateway key", map[string]string{"UPMO_GATEWAY_KEY": "upmo_svc_" + strings.TrimPrefix(gwKey, "upmo_svc_gateway_")}, "config: UPMO_GATEWAY_KEY: svcsig: a raw service master is not accepted; give the derived gateway key"},
		{"gateway key as markitdown key", map[string]string{"UPMO_MARKITDOWN_KEY": gwKey}, "config: UPMO_MARKITDOWN_KEY: svcsig: key is not a markitdown key"},
		{"metrics token as gateway key", map[string]string{"UPMO_GATEWAY_KEY": mtrToken}, "config: UPMO_GATEWAY_KEY: svcsig: key is not upmo_svc_gateway_ followed by base64 of 32 bytes"},
		{"run key as editor key", map[string]string{"UPMO_EDITOR_KEY": rnkKey}, "config: UPMO_EDITOR_KEY: editorjwt: key is not upmo_edk_ followed by base64 of 32 bytes"},
		{"editor key as run key", map[string]string{"UPMO_RUN_KEY": edKey}, "config: UPMO_RUN_KEY: runjwt: key is not upmo_rnk_ followed by base64 of 32 bytes"},
		{"previous without current", map[string]string{"UPMO_GATEWAY_KEY_PREVIOUS": gwPrev}, "config: UPMO_GATEWAY_KEY_PREVIOUS is set without UPMO_GATEWAY_KEY"},
		{"metrics without token", map[string]string{"UPMO_METRICS_ADDR": "127.0.0.1:9090"}, "config: UPMO_METRICS_TOKEN is required when UPMO_METRICS_ADDR is set"},
		{"service key as metrics token", map[string]string{"UPMO_METRICS_ADDR": "127.0.0.1:9090", "UPMO_METRICS_TOKEN": gwKey}, "config: UPMO_METRICS_TOKEN: metrics: token is not upmo_mtr_ followed by base64 of at least 32 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range base {
				env[k] = v
			}
			for k, v := range tc.env {
				env[k] = v
			}
			_, err := load(lookupFrom(env))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	env := map[string]string{"UPMO_SERVICE_KEY": "upmo_svc_x"}
	for k, v := range base {
		env[k] = v
	}
	if _, err := loadGateway(lookupFrom(env)); err == nil || err.Error() != "config: UPMO_SERVICE_KEY is not read; set UPMO_GATEWAY_KEY" {
		t.Fatalf("gateway refusal = %v", err)
	}
	delete(env, "UPMO_SERVICE_KEY")
	if _, err := loadGateway(lookupFrom(env)); err == nil || err.Error() != "config: UPMO_GATEWAY_KEY is required" {
		t.Fatalf("gateway without a key = %v", err)
	}
	env["UPMO_GATEWAY_KEY"] = gwKey
	if cfg, err := loadGateway(lookupFrom(env)); err != nil || cfg.GatewayKey != gwKey {
		t.Fatalf("gateway = %q %v", cfg.GatewayKey, err)
	}
}

// allKeys sets every key variable any process reads, each with a valid value.
func allKeys() map[string]string {
	return map[string]string{
		"UPMO_DATABASE_URL":            "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":              "http://localhost:5173",
		"UPMO_GATEWAY_KEY":             gwKey,
		"UPMO_GATEWAY_KEY_PREVIOUS":    gwPrev,
		"UPMO_SANDBOXD_KEY":            sbxKey,
		"UPMO_SANDBOXD_KEY_PREVIOUS":   sbxPrev,
		"UPMO_MARKITDOWN_KEY":          mdKey,
		"UPMO_MARKITDOWN_KEY_PREVIOUS": mdKey,
		"UPMO_EDITOR_KEY":              edKey,
		"UPMO_EDITOR_KEY_PREVIOUS":     edPrev,
		"UPMO_RUN_KEY":                 rnkKey,
		"UPMO_RUN_KEY_PREVIOUS":        rnkKey,
		"UPMO_PROVISION_KEYS":          "upmo_svc_first",
		"UPMO_METRICS_ADDR":            "127.0.0.1:9090",
		"UPMO_METRICS_TOKEN":           mtrToken,
	}
}

func TestLoadGatewayHoldsOnlyItsOwnKeys(t *testing.T) {
	cfg, err := loadGateway(lookupFrom(allKeys()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GatewayKey != gwKey || cfg.GatewayKeyPrevious != gwPrev || cfg.MetricsToken != mtrToken {
		t.Fatalf("own keys = %q %q %q", cfg.GatewayKey, cfg.GatewayKeyPrevious, cfg.MetricsToken)
	}
	for name, v := range map[string]string{
		"SandboxdKey": cfg.SandboxdKey, "SandboxdKeyPrevious": cfg.SandboxdKeyPrevious,
		"MarkitdownKey": cfg.MarkitdownKey, "MarkitdownKeyPrevious": cfg.MarkitdownKeyPrevious,
		"EditorKey": cfg.EditorKey, "EditorKeyPrevious": cfg.EditorKeyPrevious,
		"RunKey": cfg.RunKey, "RunKeyPrevious": cfg.RunKeyPrevious,
	} {
		if v != "" {
			t.Errorf("gateway %s = %q, want empty", name, v)
		}
	}
	if cfg.ProvisionKeys != nil {
		t.Errorf("gateway ProvisionKeys = %v, want nil", cfg.ProvisionKeys)
	}
	// A malformed foreign key does not stop gateway, since it never reads one.
	env := allKeys()
	env["UPMO_SANDBOXD_KEY"] = gwKey
	if _, err := loadGateway(lookupFrom(env)); err != nil {
		t.Fatalf("gateway read the sandboxd key: %v", err)
	}
}

func TestLoadSandboxdHoldsOnlyItsOwnKeys(t *testing.T) {
	cfg, err := loadSandboxd(lookupFrom(allKeys()))
	if err != nil {
		t.Fatal(err)
	}
	want := Sandboxd{
		SandboxdKey: sbxKey, SandboxdKeyPrevious: sbxPrev,
		EditorKey: edKey, EditorKeyPrevious: edPrev,
		MetricsToken: mtrToken,
	}
	if cfg.SandboxdKey != want.SandboxdKey || cfg.SandboxdKeyPrevious != want.SandboxdKeyPrevious ||
		cfg.EditorKey != want.EditorKey || cfg.EditorKeyPrevious != want.EditorKeyPrevious || cfg.MetricsToken != want.MetricsToken {
		t.Fatalf("own keys = %+v", cfg)
	}
	// No other field of Sandboxd may carry a key value from the environment.
	foreign := map[string]bool{gwKey: true, gwPrev: true, mdKey: true, rnkKey: true, "upmo_svc_first": true}
	v := reflect.ValueOf(cfg)
	for i := range v.NumField() {
		if f := v.Field(i); f.Kind() == reflect.String && foreign[f.String()] {
			t.Errorf("sandboxd %s holds a foreign key", v.Type().Field(i).Name)
		}
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	if _, err := load(lookupFrom(nil)); err == nil {
		t.Fatal("missing database URL was accepted")
	}
}

func TestLoadDatabaseAppURL(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseAppURL != "" {
		t.Fatalf("default = %q, want empty", cfg.DatabaseAppURL)
	}
	if _, err := loadBrain(lookupFrom(base)); err == nil || !strings.Contains(err.Error(), "UPMO_DATABASE_APP_URL is required") {
		t.Fatalf("brain without an app URL: %v", err)
	}
	base["UPMO_DATABASE_APP_URL"] = "postgres://upmo_app:s3cret@localhost/upmo"
	cfg, err = loadBrain(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseAppURL != "postgres://upmo_app:s3cret@localhost/upmo" { // #nosec G101 -- a fake password in a config test
		t.Fatalf("DatabaseAppURL = %q", cfg.DatabaseAppURL)
	}
	// A load error still comes through before the app URL is checked.
	delete(base, "UPMO_PUBLIC_URL")
	if _, err := loadBrain(lookupFrom(base)); err == nil || !strings.Contains(err.Error(), "UPMO_PUBLIC_URL") {
		t.Fatalf("brain without a public URL: %v", err)
	}
}

func TestLoadDatabaseURLRequiresVerifyFullOffLocalhost(t *testing.T) {
	base := map[string]string{
		"UPMO_PUBLIC_URL": "http://localhost:5173",
	}
	for _, mode := range []string{"", "disable", "allow", "prefer", "require", "verify-ca"} {
		raw := "postgres://db.example.com/upmo"
		if mode != "" {
			raw += "?sslmode=" + mode
		}
		base["UPMO_DATABASE_URL"] = raw
		_, err := load(lookupFrom(base))
		if err == nil || !strings.Contains(err.Error(), "config: UPMO_DATABASE_URL") {
			t.Errorf("sslmode=%q: err = %v, want it to name UPMO_DATABASE_URL", mode, err)
		}
	}
	base["UPMO_DATABASE_URL"] = "postgres://db.example.com/upmo?sslmode=verify-full"
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatalf("verify-full: %v", err)
	}
	if cfg.DatabaseURL != base["UPMO_DATABASE_URL"] {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	for _, exempt := range []string{
		"postgres://postgres:5432/upmo?sslmode=disable",
		"postgres://upmo@127.0.0.1:5432/upmo?sslmode=disable",
		"postgres://upmo@localhost/upmo",
	} {
		base["UPMO_DATABASE_URL"] = exempt
		if _, err := load(lookupFrom(base)); err != nil {
			t.Errorf("%s: %v", exempt, err)
		}
	}
}

func TestLoadDatabaseAppURLRequiresVerifyFullOffLocalhost(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	base["UPMO_DATABASE_APP_URL"] = "postgres://db.example.com/upmo?sslmode=disable"
	_, err := load(lookupFrom(base))
	if err == nil || !strings.Contains(err.Error(), "config: UPMO_DATABASE_APP_URL") {
		t.Fatalf("err = %v, want it to name UPMO_DATABASE_APP_URL", err)
	}
	base["UPMO_DATABASE_APP_URL"] = "postgres://db.example.com/upmo?sslmode=verify-full"
	if _, err := load(lookupFrom(base)); err != nil {
		t.Fatalf("verify-full: %v", err)
	}
}

func TestLoadServiceURLsRequireHTTPSOffLocalhost(t *testing.T) {
	for _, name := range []string{"UPMO_GATEWAY_URL", "UPMO_SANDBOXD_URL", "UPMO_MARKITDOWN_URL"} {
		base := map[string]string{
			"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
			"UPMO_PUBLIC_URL":   "http://localhost:5173",
			name:                "http://gateway.internal.example",
		}
		if _, err := load(lookupFrom(base)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s over http: err = %v, want it to name %s", name, err, name)
		}
		base[name] = "http://gateway:8080"
		if _, err := load(lookupFrom(base)); err != nil {
			t.Errorf("%s on one label: %v", name, err)
		}
		base[name] = "http://127.0.0.1:8082"
		if _, err := load(lookupFrom(base)); err != nil {
			t.Errorf("%s on loopback: %v", name, err)
		}
		base[name] = "https://gateway.example.com"
		if _, err := load(lookupFrom(base)); err != nil {
			t.Errorf("%s over https: %v", name, err)
		}
	}
}

func TestLoadMarkitdownURLEmptySkipsTheSidecar(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MarkitdownURL != "" {
		t.Fatalf("unset = %q, want empty", cfg.MarkitdownURL)
	}
	base["UPMO_MARKITDOWN_URL"] = ""
	cfg, err = load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MarkitdownURL != "" {
		t.Fatalf("empty = %q, want empty", cfg.MarkitdownURL)
	}
	base["UPMO_MARKITDOWN_URL"] = "http://markitdown:8000/"
	cfg, err = load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MarkitdownURL != "http://markitdown:8000" {
		t.Fatalf("MarkitdownURL = %q", cfg.MarkitdownURL)
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
		// The CA file is read only when mail is set.
		"UPMO_TLS_CA_FILE": "/etc/upmo/ca.pem",
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
	cfg, err = load(lookupFrom(map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
		"UPMO_SMTP_HOST":    "smtp.example.com",
		"UPMO_SMTP_FROM":    "noreply@example.com",
		"UPMO_TLS_CA_FILE":  "  /etc/upmo/ca.pem  ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.CAFile != "/etc/upmo/ca.pem" {
		t.Fatalf("CAFile = %q, want the trimmed UPMO_TLS_CA_FILE", cfg.SMTP.CAFile)
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

func TestLoadAuditRetentionDays(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditRetentionDays != 365 {
		t.Fatalf("default = %d, want 365", cfg.AuditRetentionDays)
	}
	for _, days := range []string{"30", "365", "2190", "2555", "3650"} {
		base["UPMO_AUDIT_RETENTION_DAYS"] = days
		cfg, err := load(lookupFrom(base))
		if err != nil {
			t.Fatalf("%s: %v", days, err)
		}
		if want, _ := strconv.Atoi(days); cfg.AuditRetentionDays != want {
			t.Fatalf("UPMO_AUDIT_RETENTION_DAYS=%s gave %d days", days, cfg.AuditRetentionDays)
		}
	}
	for _, days := range []string{"29", "3651", "0", "-30", "soon", "abc"} {
		base["UPMO_AUDIT_RETENTION_DAYS"] = days
		if _, err := load(lookupFrom(base)); err == nil {
			t.Errorf("%s was accepted", days)
		}
	}
}

func TestLoadAuditWebhookURL(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditWebhookURL != "" {
		t.Fatalf("default = %q, want empty", cfg.AuditWebhookURL)
	}
	base["UPMO_AUDIT_WEBHOOK_URL"] = "https://siem.example.com/audit/"
	cfg, err = load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditWebhookURL != "https://siem.example.com/audit" {
		t.Fatalf("AuditWebhookURL = %q", cfg.AuditWebhookURL)
	}
	for _, bad := range []string{
		"http://siem.example.com/audit", "siem.example.com/audit", "https://", "/audit", "ftp://siem.example.com/audit",
	} {
		base["UPMO_AUDIT_WEBHOOK_URL"] = bad
		_, err := load(lookupFrom(base))
		if err == nil {
			t.Errorf("%q was accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), bad) {
			t.Errorf("%q: the error names the value: %v", bad, err)
		}
	}
}

func TestLoadAuditSyslogAddr(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditSyslogAddr != "" {
		t.Fatalf("default = %q, want empty", cfg.AuditSyslogAddr)
	}
	base["UPMO_AUDIT_SYSLOG_ADDR"] = "siem.example.com:6514"
	cfg, err = load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditSyslogAddr != "siem.example.com:6514" {
		t.Fatalf("AuditSyslogAddr = %q", cfg.AuditSyslogAddr)
	}
	for _, bad := range []string{
		"siem.example.com", "siem.example.com:", "siem.example.com:syslog", ":6514",
		"siem.example.com:0", "siem.example.com:-1", "siem.example.com:65536",
	} {
		base["UPMO_AUDIT_SYSLOG_ADDR"] = bad
		_, err := load(lookupFrom(base))
		if err == nil {
			t.Errorf("%q was accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), bad) {
			t.Errorf("%q: the error names the value: %v", bad, err)
		}
	}
}

func TestLoadSandboxImage(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil || cfg.SandboxImage != "" {
		t.Fatalf("default = %q %v", cfg.SandboxImage, err)
	}
	base["UPMO_SANDBOX_IMAGE"] = " upmo/sandbox:base "
	cfg, err = load(lookupFrom(base))
	if err != nil || cfg.SandboxImage != "upmo/sandbox:base" {
		t.Fatalf("SandboxImage = %q %v", cfg.SandboxImage, err)
	}
	base["UPMO_SANDBOX_IMAGE"] = "upmo/sandbox:base --privileged"
	if _, err := load(lookupFrom(base)); err == nil {
		t.Fatal("sandbox image with a space")
	}
}

func TestLoadSandboxd(t *testing.T) {
	cfg, err := loadSandboxd(lookupFrom(map[string]string{"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8083" || cfg.ProxyAddr != ":3128" || cfg.ShutdownTimeout != 10*time.Second || cfg.LogLevel != "info" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.SandboxdKey != sbxKey || cfg.SandboxdKeyPrevious != "" || cfg.EditorKey != edKey || cfg.EditorKeyPrevious != "" {
		t.Fatalf("keys = %+v", cfg)
	}
	if cfg.Driver != "auto" {
		t.Fatalf("Driver = %q, want auto", cfg.Driver)
	}
	if cfg.EgressProxyURL != "http://host.docker.internal:3128" || cfg.SandboxNetwork != "upmo-sandbox-egress" {
		t.Fatalf("egress defaults = %q %q", cfg.EgressProxyURL, cfg.SandboxNetwork)
	}

	if cfg.EditorAddr != ":8084" || cfg.EditorBaseURL != "http://127.0.0.1:8084" {
		t.Fatalf("editor defaults = %q %q", cfg.EditorAddr, cfg.EditorBaseURL)
	}
	cfg, err = loadSandboxd(lookupFrom(map[string]string{
		"UPMO_SANDBOXD_KEY":          sbxKey,
		"UPMO_SANDBOXD_KEY_PREVIOUS": sbxPrev,
		"UPMO_EDITOR_KEY":            edKey,
		"UPMO_EDITOR_KEY_PREVIOUS":   edPrev,
		"UPMO_LISTEN_ADDR":           ":9000",
		"UPMO_PROXY_ADDR":            ":9001",
		"UPMO_SHUTDOWN_TIMEOUT":      "3s",
		"UPMO_LOG_LEVEL":             "debug",
		"UPMO_SANDBOX_DRIVER":        " Docker ",
		"UPMO_EGRESS_PROXY_URL":      " http://sandboxd:3128/ ",
		"UPMO_SANDBOX_NETWORK":       " upm114_sandbox-egress ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9000" || cfg.ProxyAddr != ":9001" || cfg.ShutdownTimeout != 3*time.Second || cfg.LogLevel != "debug" || cfg.SandboxdKeyPrevious != sbxPrev || cfg.EditorKeyPrevious != edPrev || cfg.Driver != "docker" {
		t.Fatalf("overrides = %+v", cfg)
	}
	if cfg.EgressProxyURL != "http://sandboxd:3128" || cfg.SandboxNetwork != "upm114_sandbox-egress" {
		t.Fatalf("egress overrides = %q %q", cfg.EgressProxyURL, cfg.SandboxNetwork)
	}

	for name, env := range map[string]map[string]string{
		"no key":             {},
		"empty key":          {"UPMO_SANDBOXD_KEY": "", "UPMO_EDITOR_KEY": edKey},
		"no editor key":      {"UPMO_SANDBOXD_KEY": sbxKey},
		"service master":     {"UPMO_SERVICE_KEY": "upmo_svc_x", "UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey},
		"gateway key":        {"UPMO_SANDBOXD_KEY": gwKey, "UPMO_EDITOR_KEY": edKey},
		"metrics no token":   {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_METRICS_ADDR": "127.0.0.1:9093"},
		"same address":       {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_LISTEN_ADDR": ":9000", "UPMO_PROXY_ADDR": ":9000"},
		"editor same listen": {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_EDITOR_ADDR": ":8083"},
		"editor same proxy":  {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_EDITOR_ADDR": ":3128"},
		"editor base https":  {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_EDITOR_BASE_URL": "https://127.0.0.1:8084"},
		"bad timeout":        {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_SHUTDOWN_TIMEOUT": "soon"},
		"bad driver":         {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_SANDBOX_DRIVER": "nomad"},
		"https proxy":        {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_EGRESS_PROXY_URL": "https://sandboxd:3128"},
		"proxy user":         {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_EGRESS_PROXY_URL": "http://box@sandboxd:3128"},
		"proxy path":         {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_EGRESS_PROXY_URL": "http://sandboxd:3128/x"},
		"proxy host":         {"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey, "UPMO_EGRESS_PROXY_URL": "sandboxd:3128"},
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

func TestLoadMetricsAddr(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	base["UPMO_METRICS_TOKEN"] = mtrToken
	for _, valid := range []string{":9090", "127.0.0.1:9090", "[::1]:9090"} {
		base["UPMO_METRICS_ADDR"] = valid
		cfg, err := load(lookupFrom(base))
		if err != nil {
			t.Fatalf("%q: %v", valid, err)
		}
		if cfg.MetricsAddr != valid {
			t.Fatalf("MetricsAddr = %q, want %q", cfg.MetricsAddr, valid)
		}
	}
	for _, bad := range []string{"9090", "127.0.0.1", "localhost:", ":0", ":65536", ":metrics", " :9090", ":8080"} {
		base["UPMO_METRICS_ADDR"] = bad
		if _, err := load(lookupFrom(base)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestLoadSandboxdMetricsAddr(t *testing.T) {
	cfg, err := loadSandboxd(lookupFrom(map[string]string{
		"UPMO_SANDBOXD_KEY":  sbxKey,
		"UPMO_EDITOR_KEY":    edKey,
		"UPMO_METRICS_ADDR":  "127.0.0.1:9093",
		"UPMO_METRICS_TOKEN": mtrToken,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsAddr != "127.0.0.1:9093" {
		t.Fatalf("MetricsAddr = %q", cfg.MetricsAddr)
	}
	for name, addr := range map[string]string{
		"invalid":      "notaport",
		"same listen":  ":8083",
		"same proxy":   ":3128",
		"same editor":  ":8084",
		"port zero":    ":0",
		"port too big": ":70000",
	} {
		if _, err := loadSandboxd(lookupFrom(map[string]string{
			"UPMO_SANDBOXD_KEY":  sbxKey,
			"UPMO_EDITOR_KEY":    edKey,
			"UPMO_METRICS_ADDR":  addr,
			"UPMO_METRICS_TOKEN": mtrToken,
		})); err == nil {
			t.Errorf("%s (%q): want an error", name, addr)
		}
	}
}

func TestLoadProvisionKeys(t *testing.T) {
	base := map[string]string{
		"UPMO_DATABASE_URL": "postgres://upmo@localhost/upmo",
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
	cfg, err := load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProvisionKeys) != 0 {
		t.Fatalf("default ProvisionKeys = %v, want empty", cfg.ProvisionKeys)
	}
	base["UPMO_PROVISION_KEYS"] = " upmo_svc_first , , upmo_svc_second "
	cfg, err = load(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProvisionKeys) != 2 || cfg.ProvisionKeys[0] != "upmo_svc_first" || cfg.ProvisionKeys[1] != "upmo_svc_second" {
		t.Fatalf("ProvisionKeys = %v", cfg.ProvisionKeys)
	}
}
