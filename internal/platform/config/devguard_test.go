package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/testutil/pgtls"
)

// The development values compose.yml embeds, rebuilt the way compose builds
// them so no literal below is one of the guarded values.
func composeMasterKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32))
}

// composeKeys are the per-audience keys, JWT keys and scrape token
// compose.yml embeds, each base64 of a different repeated byte.
func composeKeys() []Setting {
	k := func(prefix string, b byte) string {
		return prefix + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
	}
	return []Setting{
		{Name: "UPMO_GATEWAY_KEY", Value: k("upmo_svc_gateway_", 'g')},
		{Name: "UPMO_SANDBOXD_KEY", Value: k("upmo_svc_sandboxd_", 's')},
		{Name: "UPMO_MARKITDOWN_KEY", Value: k("upmo_svc_markitdown_", 'd')},
		{Name: "UPMO_EDITOR_KEY", Value: k("upmo_edk_", 'e')},
		{Name: "UPMO_RUN_KEY", Value: k("upmo_rnk_", 'r')},
		{Name: "UPMO_METRICS_TOKEN", Value: k("upmo_mtr_", 't')},
	}
}

func composeKey(name string) string {
	for _, s := range composeKeys() {
		if s.Name == name {
			return s.Value
		}
	}
	panic(name)
}

func composeDBURL() string {
	return "postgres://upmo:upmo@postgres:5432/upmo?sslmode=disable" // #nosec G101 -- the compose development URL, never a real credential
}

func composeHouseValue() string {
	return "local-dev-not-a-provider-key" // #nosec G101 -- compose.yml's development stand-in, not a real credential
}

func repeatedKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

func repeatedServiceKey(b byte) string {
	return "upmo_svc_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// freshKey returns 32 random bytes, standard base64: a master key no rule
// refuses.
func freshKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func freshPassword(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func freshDBURL(t *testing.T) string {
	t.Helper()
	return pgtls.URL(t, "upmo:"+freshPassword(t))
}

func TestParseEnv(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Env
	}{
		{"", EnvProduction},
		{"production", EnvProduction},
		{" PRODUCTION ", EnvProduction},
		{"Development", EnvDevelopment},
		{" development ", EnvDevelopment},
	} {
		got, err := ParseEnv(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseEnv(%q) = %v, %v, want %v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"dev", "prod", "staging", "production development"} {
		_, err := ParseEnv(in)
		if err == nil {
			t.Errorf("ParseEnv(%q) was accepted", in)
			continue
		}
		const want = "config: UPMO_ENV must be production or development"
		if err.Error() != want {
			t.Errorf("ParseEnv(%q) err = %q, want %q", in, err, want)
		}
		// "dev" and "prod" sit inside the fixed message; any other input must
		// not appear in it at all.
		if !strings.Contains(want, in) && strings.Contains(err.Error(), in) {
			t.Errorf("ParseEnv(%q) err echoes the value: %v", in, err)
		}
	}
}

func TestKnownDevSecret(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"compose master key", composeMasterKey()},
		{"database owner", "upmo"},
		{"database app role", "upmo_app"},
		{"house credential", composeHouseValue()},
	} {
		if !knownDevSecret(tc.value) {
			t.Errorf("knownDevSecret(%s) = false, want true", tc.name)
		}
		if knownDevSecret(tc.value + "x") {
			t.Errorf("knownDevSecret(%s with a suffix) = true", tc.name)
		}
	}
	for _, c := range composeKeys() {
		if !knownDevSecret(c.Value) {
			t.Errorf("knownDevSecret(compose %s) = false, want true", c.Name)
		}
	}
	if knownDevSecret(freshKey(t)) {
		t.Error("knownDevSecret(fresh key) = true")
	}
}

func TestUniformBytes(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want bool
	}{
		{nil, false},
		{[]byte{}, false},
		{[]byte{7}, true},
		{bytes.Repeat([]byte{0x00}, 32), true},
		{bytes.Repeat([]byte{0x41}, 32), true},
		{[]byte{1, 1, 1, 2}, false},
	} {
		if got := uniformBytes(tc.in); got != tc.want {
			t.Errorf("uniformBytes(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCheckGuardProductionRefuses(t *testing.T) {
	refuses := func(t *testing.T, g Guard, lookup func(string) (string, bool), environ []string, wantName, secret string) {
		t.Helper()
		err := CheckGuard(g, lookup, environ)
		if err == nil {
			t.Fatalf("CheckGuard accepted %s", wantName)
		}
		if !strings.Contains(err.Error(), wantName) {
			t.Fatalf("err = %v, want it to name %s", err, wantName)
		}
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("err holds the value: %v", err)
		}
	}
	t.Run("compose master key", func(t *testing.T) {
		refuses(t, Guard{Env: EnvProduction},
			lookupFrom(map[string]string{"UPMO_MASTER_KEY": composeMasterKey()}),
			nil, "UPMO_MASTER_KEY uses a development or weak key", composeMasterKey())
	})
	t.Run("uniform master keys", func(t *testing.T) {
		for _, b := range []byte{0x00, 0x41} {
			key := repeatedKey(b)
			refuses(t, Guard{Env: EnvProduction},
				lookupFrom(map[string]string{"UPMO_MASTER_KEY": key}),
				nil, "UPMO_MASTER_KEY uses a development or weak key", key)
		}
	})
	t.Run("previous master key", func(t *testing.T) {
		refuses(t, Guard{Env: EnvProduction},
			lookupFrom(nil),
			[]string{"UPMO_MASTER_KEY_PREVIOUS_k0=" + composeMasterKey()},
			"UPMO_MASTER_KEY_PREVIOUS_k0 uses a development or weak key", composeMasterKey())
	})
	t.Run("service keys", func(t *testing.T) {
		keys := append(composeKeys(),
			Setting{Name: "UPMO_GATEWAY_KEY_PREVIOUS", Value: "upmo_svc_gateway_" + strings.TrimPrefix(repeatedServiceKey('x'), "upmo_svc_")},
			Setting{Name: "UPMO_EDITOR_KEY_PREVIOUS", Value: "upmo_edk_" + strings.TrimPrefix(repeatedServiceKey('y'), "upmo_svc_")},
			Setting{Name: "UPMO_PROVISION_KEYS", Value: repeatedServiceKey('z')},
		)
		for _, s := range keys {
			refuses(t, Guard{Env: EnvProduction, ServiceKeys: []Setting{s}},
				lookupFrom(nil), nil, s.Name+" uses a development or weak key", s.Value)
		}
	})
	t.Run("database urls", func(t *testing.T) {
		for _, tc := range []struct {
			name, url, password string
		}{
			{"owner", composeDBURL(), "upmo"},
			{"app role", "postgres://upmo_app:upmo_app@postgres:5432/upmo?sslmode=disable", "upmo_app"},
			{"gateway role", "postgres://upmo_gateway:upmo_gateway@postgres:5432/upmo?sslmode=disable", "upmo_gateway"},
			{"sandbox role", "postgres://upmo_sandbox:upmo_sandbox@postgres:5432/upmo?sslmode=disable", "upmo_sandbox"},
			{"runner role", "postgres://upmo_runner:upmo_runner@postgres:5432/upmo?sslmode=disable", "upmo_runner"},
			{"empty password", "postgres://upmo@postgres:5432/upmo?sslmode=disable", ""},
		} {
			urls := []Setting{{Name: "UPMO_DATABASE_URL", Value: tc.url}}
			refuses(t, Guard{Env: EnvProduction, DatabaseURLs: urls},
				lookupFrom(nil), nil,
				"UPMO_DATABASE_URL uses a development or empty password", tc.password)
			if err := CheckGuard(Guard{Env: EnvProduction, DatabaseURLs: urls}, lookupFrom(nil), nil); strings.Contains(err.Error(), tc.url) {
				t.Fatalf("%s: err holds the url: %v", tc.name, err)
			}
		}
	})
	t.Run("credential", func(t *testing.T) {
		refuses(t, Guard{Env: EnvProduction},
			lookupFrom(nil),
			[]string{"UPMO_CREDENTIAL_house=" + composeHouseValue()},
			"UPMO_CREDENTIAL_house uses a development value", composeHouseValue())
	})
	t.Run("missing master key", func(t *testing.T) {
		refuses(t, Guard{Env: EnvProduction, RequireMasterKey: true},
			lookupFrom(nil), nil,
			"UPMO_MASTER_KEY is required; set a key", "")
	})
	t.Run("unparseable url left to the existing checks", func(t *testing.T) {
		urls := []Setting{{Name: "UPMO_DATABASE_URL", Value: "not a url at all"}}
		if err := CheckGuard(Guard{Env: EnvProduction, DatabaseURLs: urls}, lookupFrom(nil), nil); err != nil {
			t.Fatalf("err = %v, want the existing checks to reject it", err)
		}
	})
}

func TestCheckGuardProductionAccepts(t *testing.T) {
	g := Guard{
		Env:          EnvProduction,
		DatabaseURLs: []Setting{{Name: "UPMO_DATABASE_URL", Value: freshDBURL(t)}},
		ServiceKeys: []Setting{
			{Name: "UPMO_GATEWAY_KEY", Value: "upmo_svc_gateway_" + freshKey(t)},
			{Name: "UPMO_EDITOR_KEY", Value: "upmo_edk_" + freshKey(t)},
			{Name: "UPMO_PROVISION_KEYS", Value: "upmo_svc_" + freshKey(t)},
		},
		RequireMasterKey: true,
	}
	lookup := lookupFrom(map[string]string{"UPMO_MASTER_KEY": freshKey(t)})
	environ := []string{"UPMO_CREDENTIAL_house=sk-real-provider-key"}
	if err := CheckGuard(g, lookup, environ); err != nil {
		t.Fatal(err)
	}
}

func TestCheckGuardDevelopment(t *testing.T) {
	lookup := lookupFrom(map[string]string{"UPMO_MASTER_KEY": composeMasterKey()})
	g := Guard{
		Env:              EnvDevelopment,
		PublicURL:        "http://localhost:5173",
		DatabaseURLs:     []Setting{{Name: "UPMO_DATABASE_URL", Value: composeDBURL()}},
		ServiceKeys:      composeKeys(),
		RequireMasterKey: true,
	}
	environ := []string{"UPMO_CREDENTIAL_house=" + composeHouseValue()}
	if err := CheckGuard(g, lookup, environ); err != nil {
		t.Fatalf("development with the compose values: %v", err)
	}
	if err := CheckGuard(g, lookupFrom(nil), environ); err == nil || err.Error() != "config: UPMO_MASTER_KEY is required; set a key" {
		t.Fatalf("development missing-key err = %v", err)
	}

	g.PublicURL = "https://upmo.example.com"
	err := CheckGuard(g, lookup, environ)
	if err == nil || err.Error() != "config: UPMO_ENV=development is refused with a public https UPMO_PUBLIC_URL" {
		t.Fatalf("public https err = %v", err)
	}

	for _, public := range []string{"http://localhost:5173", "https://localhost", ""} {
		g.PublicURL = public
		if err := CheckGuard(g, lookup, environ); err != nil {
			t.Errorf("public URL %q: %v", public, err)
		}
	}
}

// unsetMasterKey unsets UPMO_MASTER_KEY for the rest of t; t.Setenv
// restores it after.
func unsetMasterKey(t *testing.T) {
	t.Helper()
	t.Setenv("UPMO_MASTER_KEY", "")
	if err := os.Unsetenv("UPMO_MASTER_KEY"); err != nil {
		t.Fatal(err)
	}
}

// The exported loaders read the real environment, so their guard runs on
// t.Setenv values. An empty UPMO_ENV is production, like an unset one.
func TestLoadRefusesDevelopmentValuesInProduction(t *testing.T) {
	base := func(t *testing.T) {
		t.Helper()
		t.Setenv("UPMO_ENV", "")
		t.Setenv("UPMO_DATABASE_URL", freshDBURL(t))
		t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
		t.Setenv("UPMO_MASTER_KEY", freshKey(t))
		t.Setenv("UPMO_WORKER_URL", "https://worker.example.com")
		t.Setenv("UPMO_WORKER_BRAIN_KEY", "upmo_svc_worker-brain_"+freshKey(t))
	}
	for _, tc := range []struct {
		name, key, value, want string
	}{
		{"master key", "UPMO_MASTER_KEY", composeMasterKey(), "UPMO_MASTER_KEY uses a development or weak key"},
		{"credential", "UPMO_CREDENTIAL_house", composeHouseValue(), "UPMO_CREDENTIAL_house uses a development value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load err = %v, want it to say %q", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Fatalf("Load err holds the value: %v", err)
			}
		})
	}
	for _, c := range composeKeys() {
		t.Run(c.Name, func(t *testing.T) {
			base(t)
			t.Setenv(c.Name, c.Value)
			_, err := Load()
			want := "config: " + c.Name + " uses a development or weak key; set a unique key or UPMO_ENV=development"
			if err == nil || err.Error() != want {
				t.Fatalf("Load err = %v, want %q", err, want)
			}
		})
	}
	t.Run("database url", func(t *testing.T) {
		base(t)
		t.Setenv("UPMO_DATABASE_URL", pgtls.URL(t, "upmo:upmo"))
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "UPMO_DATABASE_URL uses a development or empty password") {
			t.Fatalf("Load err = %v", err)
		}
		if strings.Contains(err.Error(), "upmo:upmo") {
			t.Fatalf("Load err holds the credentials: %v", err)
		}
	})
	// Brain holds no master key: unset starts, and any value, even an empty
	// one or a previous key alone, is refused.
	t.Run("brain master key", func(t *testing.T) {
		base(t)
		t.Setenv("UPMO_DATABASE_APP_URL", freshDBURL(t))
		t.Setenv("UPMO_DATABASE_RUNNER_URL", freshDBURL(t))
		for _, value := range []string{freshKey(t), ""} {
			t.Setenv("UPMO_MASTER_KEY", value)
			if _, err := LoadBrain(); err == nil || err.Error() != "config: UPMO_MASTER_KEY is refused" {
				t.Fatalf("LoadBrain with UPMO_MASTER_KEY=%q err = %v", value, err)
			}
		}
		unsetMasterKey(t)
		if _, err := LoadBrain(); err != nil {
			t.Fatalf("LoadBrain without a master key: %v", err)
		}
		t.Setenv("UPMO_MASTER_KEY_PREVIOUS_k0", freshKey(t))
		if _, err := LoadBrain(); err == nil || err.Error() != "config: UPMO_MASTER_KEY is refused" {
			t.Fatalf("LoadBrain with a previous key alone err = %v", err)
		}
	})
	t.Run("development missing master key", func(t *testing.T) {
		base(t)
		t.Setenv("UPMO_ENV", "development")
		t.Setenv("UPMO_DATABASE_URL", composeDBURL())
		t.Setenv("UPMO_DATABASE_APP_URL", "postgres://upmo_app:upmo_app@postgres:5432/upmo?sslmode=disable")
		t.Setenv("UPMO_DATABASE_RUNNER_URL", "postgres://upmo_runner:upmo_runner@postgres:5432/upmo?sslmode=disable")
		t.Setenv("UPMO_MASTER_KEY", "")
		if _, err := Load(); err == nil || err.Error() != "config: UPMO_MASTER_KEY is required; set a key" {
			t.Errorf("development missing-key err = %v", err)
		}
		unsetMasterKey(t)
		if _, err := LoadBrain(); err != nil {
			t.Errorf("development brain without a master key: %v", err)
		}
	})
}

func TestLoadAcceptsFreshValuesInProduction(t *testing.T) {
	t.Setenv("UPMO_ENV", "")
	t.Setenv("UPMO_DATABASE_URL", freshDBURL(t))
	t.Setenv("UPMO_DATABASE_APP_URL", freshDBURL(t))
	t.Setenv("UPMO_DATABASE_RUNNER_URL", freshDBURL(t))
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("UPMO_MASTER_KEY", freshKey(t))
	t.Setenv("UPMO_GATEWAY_KEY", "upmo_svc_gateway_"+freshKey(t))
	t.Setenv("UPMO_RUN_KEY", "upmo_rnk_"+freshKey(t))
	t.Setenv("UPMO_CREDENTIAL_house", "sk-real-provider-key")
	t.Setenv("UPMO_WORKER_URL", "https://worker.example.com")
	t.Setenv("UPMO_WORKER_BRAIN_KEY", "upmo_svc_worker-brain_"+freshKey(t))
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	unsetMasterKey(t)
	if _, err := LoadBrain(); err != nil {
		t.Fatalf("LoadBrain: %v", err)
	}
}

func TestLoadDevelopmentRule(t *testing.T) {
	t.Setenv("UPMO_ENV", "development")
	t.Setenv("UPMO_DATABASE_URL", composeDBURL())
	t.Setenv("UPMO_DATABASE_APP_URL", "postgres://upmo_app:upmo_app@postgres:5432/upmo?sslmode=disable")
	t.Setenv("UPMO_DATABASE_RUNNER_URL", "postgres://upmo_runner:upmo_runner@postgres:5432/upmo?sslmode=disable")
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("UPMO_MASTER_KEY", composeMasterKey())
	for _, c := range composeKeys() {
		t.Setenv(c.Name, c.Value)
	}
	t.Setenv("UPMO_CREDENTIAL_house", composeHouseValue())
	t.Setenv("UPMO_WORKER_URL", "http://worker:8090")
	t.Setenv("UPMO_WORKER_BRAIN_KEY", "upmo_svc_worker-brain_"+freshKey(t))
	unsetMasterKey(t)
	if _, err := LoadBrain(); err != nil {
		t.Fatalf("LoadBrain with the compose values in development: %v", err)
	}
	t.Setenv("UPMO_MASTER_KEY", composeMasterKey())
	t.Setenv("UPMO_PUBLIC_URL", "https://upmo.example.com")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "config: UPMO_ENV=development is refused with a public https UPMO_PUBLIC_URL") {
		t.Fatalf("public https err = %v", err)
	}
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	if _, err := Load(); err != nil {
		t.Fatalf("loopback public URL in development: %v", err)
	}
}

func TestLoadSandboxdGuardsItsKeys(t *testing.T) {
	fresh := func(t *testing.T) {
		t.Helper()
		t.Setenv("UPMO_ENV", "")
		t.Setenv("UPMO_SANDBOXD_KEY", "upmo_svc_sandboxd_"+freshKey(t))
		t.Setenv("UPMO_EDITOR_KEY", "upmo_edk_"+freshKey(t))
	}
	for _, name := range []string{"UPMO_SANDBOXD_KEY", "UPMO_EDITOR_KEY", "UPMO_METRICS_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			fresh(t)
			t.Setenv(name, composeKey(name))
			_, err := LoadSandboxd()
			if err == nil || !strings.Contains(err.Error(), name+" uses a development or weak key") {
				t.Fatalf("LoadSandboxd err = %v", err)
			}
			if strings.Contains(err.Error(), composeKey(name)) {
				t.Fatalf("LoadSandboxd err holds the key: %v", err)
			}
			t.Setenv("UPMO_ENV", "development")
			if _, err := LoadSandboxd(); err != nil {
				t.Fatalf("LoadSandboxd in development: %v", err)
			}
		})
	}
	fresh(t)
	if _, err := LoadSandboxd(); err != nil {
		t.Fatalf("LoadSandboxd with fresh keys: %v", err)
	}
}
