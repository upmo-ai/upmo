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
	// compose.yml UPMO_SERVICE_KEY.
	"df195a9a71e585f465f184ceb91bd77b416683aebbfcbc2ebf7da36f828d04b5": true,
	// The compose database owner password.
	"07c69b4fc5934597a9301d5fa7ce7d79ecc1791a71a67aa50a03f08f6a9b29ce": true,
	// The compose database app-role password.
	"f5462226c6de4a946f199fb284973f1a5e54a50c80bc631fdab4586fa8120045": true,
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
	// ServiceKeys are UPMO_SERVICE_KEY, UPMO_SERVICE_KEY_PREVIOUS and each
	// UPMO_PROVISION_KEYS entry, only the non-empty ones.
	ServiceKeys []Setting
	// RequireMasterKey refuses a missing UPMO_MASTER_KEY: brain and gateway
	// seal credentials with it, so they need one in production.
	RequireMasterKey bool
}

// CheckGuard refuses the development keys and passwords compose.yml embeds —
// and the weak keys that reduce to them — unless Env is development. Errors
// name the variable and the rule, never the value. In development a public
// https origin is refused instead: the relaxed rules are for the local stack.
func CheckGuard(g Guard, lookup func(string) (string, bool), environ []string) error {
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
	if g.RequireMasterKey {
		if v, ok := lookup("UPMO_MASTER_KEY"); !ok || strings.TrimSpace(v) == "" {
			return fmt.Errorf("config: UPMO_MASTER_KEY is required in production; set a key or UPMO_ENV=development")
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

// checkServiceKey refuses a service key from compose.yml or one whose payload
// is all equal bytes. The decoding mirrors svcsig.ParseKey (upmo_svc_ plus
// base64 of at least 32 bytes); a value that does not parse is left for the
// service-key code to reject.
func checkServiceKey(name, v string) error {
	if knownDevSecret(v) {
		return weakKeyError(name)
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(v), "upmo_svc_")
	if !ok {
		return nil
	}
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(rest, "="))
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

// CheckConfig runs the guard for a loaded Config. requireMasterKey is true for
// brain and gateway, which seal credentials with UPMO_MASTER_KEY.
func CheckConfig(cfg Config, requireMasterKey bool, lookup func(string) (string, bool), environ []string) error {
	g := Guard{
		Env:              cfg.Env,
		PublicURL:        cfg.PublicURL,
		RequireMasterKey: requireMasterKey,
	}
	if cfg.DatabaseURL != "" {
		g.DatabaseURLs = append(g.DatabaseURLs, Setting{Name: "UPMO_DATABASE_URL", Value: cfg.DatabaseURL})
	}
	if cfg.DatabaseAppURL != "" {
		g.DatabaseURLs = append(g.DatabaseURLs, Setting{Name: "UPMO_DATABASE_APP_URL", Value: cfg.DatabaseAppURL})
	}
	if cfg.ServiceKey != "" {
		g.ServiceKeys = append(g.ServiceKeys, Setting{Name: "UPMO_SERVICE_KEY", Value: cfg.ServiceKey})
	}
	if cfg.ServiceKeyPrevious != "" {
		g.ServiceKeys = append(g.ServiceKeys, Setting{Name: "UPMO_SERVICE_KEY_PREVIOUS", Value: cfg.ServiceKeyPrevious})
	}
	for _, key := range cfg.ProvisionKeys {
		if key != "" {
			g.ServiceKeys = append(g.ServiceKeys, Setting{Name: "UPMO_PROVISION_KEYS", Value: key})
		}
	}
	return CheckGuard(g, lookup, environ)
}

// CheckSandboxd runs the guard for a loaded Sandboxd config: service keys
// only, since sandboxd opens no database and seals nothing.
func CheckSandboxd(cfg Sandboxd, lookup func(string) (string, bool), environ []string) error {
	g := Guard{Env: cfg.Env}
	if cfg.ServiceKey != "" {
		g.ServiceKeys = append(g.ServiceKeys, Setting{Name: "UPMO_SERVICE_KEY", Value: cfg.ServiceKey})
	}
	if cfg.ServiceKeyPrevious != "" {
		g.ServiceKeys = append(g.ServiceKeys, Setting{Name: "UPMO_SERVICE_KEY_PREVIOUS", Value: cfg.ServiceKeyPrevious})
	}
	return CheckGuard(g, lookup, environ)
}
