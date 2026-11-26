package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
)

// Env is the deployment environment a process runs in (UPMO_ENV). Production
// is the default; development relaxes the guard below for the local stack.
type Env string

// The two environments UPMO_ENV accepts.
const (
	EnvProduction  Env = "production"
	EnvDevelopment Env = "development"
)

// ParseEnv reads UPMO_ENV: unset or empty is production, and both values are
// accepted in any case with surrounding space trimmed. Anything else is a
// configuration error that never echoes the value.
func ParseEnv(v string) (Env, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", string(EnvProduction):
		return EnvProduction, nil
	case string(EnvDevelopment):
		return EnvDevelopment, nil
	default:
		return "", fmt.Errorf("config: UPMO_ENV must be production or development")
	}
}

// knownDevDigests are the SHA-256 digests of the development keys and
// passwords compose.yml embeds. Only digests are kept so no known value
// appears here as a literal; comparisons are on the trimmed value.
var knownDevDigests = map[string]bool{
	// compose.yml UPMO_MASTER_KEY (base64 of 32 equal bytes).
	"c8cb79a67c6351c9eea0016493e4b902e2d5d718c2f7641cd6daeec215ac6ff9": true,
	// compose.yml UPMO_GATEWAY_KEY.
	"c146b9e307eb13829c13a0758c6fdadddcd10edb7019a049f51bbc7d81d21bfd": true,
	// compose.yml UPMO_SANDBOXD_KEY.
	"37d3c698d078e796f1cc10396948e8e5aad7925bd77cd3077dea3d357ac64533": true,
	// compose.yml UPMO_MARKITDOWN_KEY.
	"3c22fa382072b44da164bc05df41f31ba9004d8f479353544b806e208a7bc456": true,
	// compose.yml UPMO_WORKER_BRAIN_KEY.
	"4c81959230ff750ac463bc5ad481ed64565926696eeb64340dc8a841f281a280": true,
	// compose.yml UPMO_WORKER_GATEWAY_KEY.
	"62fe3c3daac177e35e7832b2b69edaea19a749084d0af242dbb6a7be22b7b210": true,
	// compose.yml UPMO_WORKER_SANDBOXD_KEY.
	"cc510ce49c5643208f8671afa8241a44eeeb277a4da3230c5ddbccc82cd24f9b": true,
	// compose.yml UPMO_EDITOR_KEY.
	"a56282befedd89a4c6fb3ae7e34e16cbfe20680c0606581babf500cbe4adf1c6": true,
	// compose.yml UPMO_RUN_KEY.
	"543f20b476c8c809bce352d030e5d17dc31a6031f0f523de52c58ef9e73ad17b": true,
	// compose.yml UPMO_METRICS_TOKEN (commented out there).
	"79a1e4ecbbb1ba0268b2fff520b18d5a61a79854f76daac1129f08327b085fb5": true,
	// The compose database owner password.
	"07c69b4fc5934597a9301d5fa7ce7d79ecc1791a71a67aa50a03f08f6a9b29ce": true,
	// The compose database app-role password.
	"f5462226c6de4a946f199fb284973f1a5e54a50c80bc631fdab4586fa8120045": true,
	// The compose database gateway-role password.
	"9bafec2c8b2b71262605db1c20f82924bbc88af30598fcc12fb4e9833e1bd609": true,
	// The compose database sandbox-role password.
	"40138018dd8f4de2facbfe2a27ee3fc883005d89832ab1cbb87b12f834fec730": true,
	// The compose database runner-role password.
	"0c4abf20162db3c903e946e75ca7d770cb1e4f0e835bff4395e0ce0c307ecb6b": true,
	// compose.yml UPMO_CREDENTIAL_house.
	"b6b52de7d120f1f660456fc30084ecf688c237e021b30a52ec55968732aada18": true,
}

// knownDevSecret reports whether v is one of the development values compose
// embeds, compared by digest so the values never appear in code or errors.
func knownDevSecret(v string) bool {
	sum := sha256.Sum256([]byte(strings.TrimSpace(v)))
	return knownDevDigests[hex.EncodeToString(sum[:])]
}

// uniformBytes reports whether b is non-empty and every byte equals the first.
// A key or password made of one repeated byte is development-grade.
func uniformBytes(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, x := range b[1:] {
		if x != b[0] {
			return false
		}
	}
	return true
}

// Setting is one configured value the guard checks, carrying the variable
// name its errors must name.
type Setting struct{ Name, Value string }

// Guard is everything the development-key guard decides on. Master keys and
// credentials are not carried here: the guard reads them from lookup and
// environ itself, because the keyring and the credential chain read the same
// variables there.
type Guard struct {
	// Env is the parsed UPMO_ENV.
	Env Env
	// PublicURL is the browser origin (UPMO_PUBLIC_URL); empty skips the
	// development public-origin rule.
	PublicURL string
	// DatabaseURLs are the Postgres URLs to check, only the non-empty ones.
	DatabaseURLs []Setting
	// ServiceKeys are the audience, editor and run keys with their
	// _PREVIOUS partners, UPMO_METRICS_TOKEN and each UPMO_PROVISION_KEYS
	// entry, only the non-empty ones.
	ServiceKeys []Setting
	// RequireMasterKey refuses a missing UPMO_MASTER_KEY: callers that load
	// secrets need one in every environment, including development.
	RequireMasterKey bool
}

// CheckGuard refuses the development keys and passwords compose.yml embeds —
// and the weak keys that reduce to them — unless Env is development. Errors
// name the variable and the rule, never the value. In development a public
// https origin is refused instead: the relaxed rules are for the local stack.
func CheckGuard(g Guard, lookup func(string) (string, bool), environ []string) error {
	if g.RequireMasterKey {
		if v, ok := lookup("UPMO_MASTER_KEY"); !ok || strings.TrimSpace(v) == "" {
			return fmt.Errorf("config: UPMO_MASTER_KEY is required; set a key")
		}
	}
	if g.Env == EnvDevelopment {
		if u, err := url.Parse(g.PublicURL); err == nil && u.Scheme == "https" && !tlscheck.ExemptHost(u.Hostname()) {
			return fmt.Errorf("config: UPMO_ENV=development is refused with a public https UPMO_PUBLIC_URL")
		}
		return nil
	}
	if v, ok := lookup("UPMO_MASTER_KEY"); ok && strings.TrimSpace(v) != "" {
		if err := checkMasterKey("UPMO_MASTER_KEY", v); err != nil {
			return err
		}
	}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if id, isPrevious := strings.CutPrefix(name, "UPMO_MASTER_KEY_PREVIOUS_"); isPrevious {
			if err := checkMasterKey("UPMO_MASTER_KEY_PREVIOUS_"+id, value); err != nil {
				return err
			}
		}
	}
	for _, s := range g.ServiceKeys {
		if err := checkServiceKey(s.Name, s.Value); err != nil {
			return err
		}
	}
	for _, s := range g.DatabaseURLs {
		pgcfg, err := pgconn.ParseConfig(s.Value)
		if err != nil {
			continue // the existing URL checks reject what pgx cannot parse
		}
		if pgcfg.Password == "" || knownDevSecret(pgcfg.Password) {
			return fmt.Errorf("config: %s uses a development or empty password; set a unique password or UPMO_ENV=development", s.Name)
		}
	}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if _, isCredential := strings.CutPrefix(name, "UPMO_CREDENTIAL_"); isCredential && knownDevSecret(value) {
			return fmt.Errorf("config: %s uses a development value; set a real value or UPMO_ENV=development", name)
		}
	}
	return nil
}

// checkMasterKey refuses a master key from compose.yml or any key that decodes
// to 32 equal bytes. The decoding mirrors the keyring (trimmed standard
// base64 of exactly 32 bytes); a value that does not decode is left for the
// keyring to reject.
func checkMasterKey(name, v string) error {
	if knownDevSecret(v) {
		return weakKeyError(name)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil || len(raw) != 32 {
		return nil
	}
	if uniformBytes(raw) {
		return weakKeyError(name)
	}
	return nil
}

// checkServiceKey refuses a key from compose.yml or one whose payload is all
// equal bytes. Every key is upmo_ text ending in _ and base64, and base64 has
// no underscore, so the payload is what follows the last underscore. A value
// that does not decode is left for its parser to reject.
func checkServiceKey(name, v string) error {
	if knownDevSecret(v) {
		return weakKeyError(name)
	}
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "upmo_") {
		return nil
	}
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(v[strings.LastIndex(v, "_")+1:], "="))
	if err != nil || len(key) < 32 {
		return nil
	}
	if uniformBytes(key) {
		return weakKeyError(name)
	}
	return nil
}

func weakKeyError(name string) error {
	return fmt.Errorf("config: %s uses a development or weak key; set a unique key or UPMO_ENV=development", name)
}

// CheckConfig runs the guard for a loaded Config. Secret-using callers require
// UPMO_MASTER_KEY in every environment.
func CheckConfig(cfg Config, requireMasterKey bool, lookup func(string) (string, bool), environ []string) error {
	g := Guard{
		Env:              cfg.Env,
		PublicURL:        cfg.PublicURL,
		RequireMasterKey: requireMasterKey,
	}
	g.DatabaseURLs = nonEmpty(
		Setting{Name: "UPMO_DATABASE_URL", Value: cfg.DatabaseURL},
		Setting{Name: "UPMO_DATABASE_APP_URL", Value: cfg.DatabaseAppURL},
		Setting{Name: "UPMO_DATABASE_RUNNER_URL", Value: cfg.DatabaseRunnerURL},
		Setting{Name: "UPMO_DATABASE_GATEWAY_URL", Value: cfg.DatabaseGatewayURL},
		Setting{Name: "UPMO_DATABASE_SANDBOX_URL", Value: cfg.DatabaseSandboxURL},
	)
	g.ServiceKeys = append(g.ServiceKeys, nonEmpty(
		Setting{Name: "UPMO_GATEWAY_KEY", Value: cfg.GatewayKey},
		Setting{Name: "UPMO_GATEWAY_KEY_PREVIOUS", Value: cfg.GatewayKeyPrevious},
		Setting{Name: "UPMO_SANDBOXD_KEY", Value: cfg.SandboxdKey},
		Setting{Name: "UPMO_SANDBOXD_KEY_PREVIOUS", Value: cfg.SandboxdKeyPrevious},
		Setting{Name: "UPMO_MARKITDOWN_KEY", Value: cfg.MarkitdownKey},
		Setting{Name: "UPMO_MARKITDOWN_KEY_PREVIOUS", Value: cfg.MarkitdownKeyPrevious},
		Setting{Name: "UPMO_EDITOR_KEY", Value: cfg.EditorKey},
		Setting{Name: "UPMO_EDITOR_KEY_PREVIOUS", Value: cfg.EditorKeyPrevious},
		Setting{Name: "UPMO_RUN_KEY", Value: cfg.RunKey},
		Setting{Name: "UPMO_RUN_KEY_PREVIOUS", Value: cfg.RunKeyPrevious},
		Setting{Name: "UPMO_METRICS_TOKEN", Value: cfg.MetricsToken},
		Setting{Name: "UPMO_WORKER_BRAIN_KEY", Value: cfg.WorkerBrainKey},
		Setting{Name: "UPMO_WORKER_BRAIN_KEY_PREVIOUS", Value: cfg.WorkerBrainKeyPrevious},
		Setting{Name: "UPMO_WORKER_GATEWAY_KEY", Value: cfg.WorkerGatewayKey},
		Setting{Name: "UPMO_WORKER_GATEWAY_KEY_PREVIOUS", Value: cfg.WorkerGatewayKeyPrevious},
		Setting{Name: "UPMO_WORKER_SANDBOXD_KEY", Value: cfg.WorkerSandboxdKey},
		Setting{Name: "UPMO_WORKER_SANDBOXD_KEY_PREVIOUS", Value: cfg.WorkerSandboxdKeyPrevious},
	)...)
	for _, key := range cfg.ProvisionKeys {
		if key != "" {
			g.ServiceKeys = append(g.ServiceKeys, Setting{Name: "UPMO_PROVISION_KEYS", Value: key})
		}
	}
	return CheckGuard(g, lookup, environ)
}

// CheckSandboxd runs the guard for a loaded Sandboxd config: its keys and
// its database URL, since sandboxd seals nothing.
func CheckSandboxd(cfg Sandboxd, lookup func(string) (string, bool), environ []string) error {
	g := Guard{Env: cfg.Env, DatabaseURLs: nonEmpty(
		Setting{Name: "UPMO_DATABASE_SANDBOX_URL", Value: cfg.DatabaseSandboxURL},
	), ServiceKeys: nonEmpty(
		Setting{Name: "UPMO_SANDBOXD_KEY", Value: cfg.SandboxdKey},
		Setting{Name: "UPMO_SANDBOXD_KEY_PREVIOUS", Value: cfg.SandboxdKeyPrevious},
		Setting{Name: "UPMO_EDITOR_KEY", Value: cfg.EditorKey},
		Setting{Name: "UPMO_EDITOR_KEY_PREVIOUS", Value: cfg.EditorKeyPrevious},
		Setting{Name: "UPMO_METRICS_TOKEN", Value: cfg.MetricsToken},
		Setting{Name: "UPMO_WORKER_SANDBOXD_KEY", Value: cfg.WorkerSandboxdKey},
		Setting{Name: "UPMO_WORKER_SANDBOXD_KEY_PREVIOUS", Value: cfg.WorkerSandboxdKeyPrevious},
	)}
	return CheckGuard(g, lookup, environ)
}

func nonEmpty(settings ...Setting) []Setting {
	var out []Setting
	for _, s := range settings {
		if s.Value != "" {
			out = append(out, s)
		}
	}
	return out
}
