package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// The development values compose.yml embeds, rebuilt the way compose builds
// them so no literal below is one of the guarded values.
func composeMasterKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32))
}

func composeServiceKey() string {
	return "upmo_svc_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'g'}, 32))
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
	return "postgres://upmo:" + freshPassword(t) + "@postgres:5432/upmo?sslmode=disable"
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
		{"compose service key", composeServiceKey()},
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
		keys := []Setting{
			{Name: "UPMO_SERVICE_KEY", Value: composeServiceKey()},
			{Name: "UPMO_SERVICE_KEY_PREVIOUS", Value: repeatedServiceKey('x')},
			{Name: "UPMO_PROVISION_KEYS", Value: repeatedServiceKey('z')},
		}
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
			"UPMO_MASTER_KEY is required in production", "")
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
		Env:              EnvProduction,
		DatabaseURLs:     []Setting{{Name: "UPMO_DATABASE_URL", Value: freshDBURL(t)}},
		ServiceKeys:      []Setting{{Name: "UPMO_SERVICE_KEY", Value: "upmo_svc_" + freshKey(t)}},
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
		ServiceKeys:      []Setting{{Name: "UPMO_SERVICE_KEY", Value: composeServiceKey()}},
		RequireMasterKey: true,
	}
	environ := []string{"UPMO_CREDENTIAL_house=" + composeHouseValue()}
	if err := CheckGuard(g, lookup, environ); err != nil {
		t.Fatalf("development with the compose values: %v", err)
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

// The exported loaders read the real environment, so their guard runs on
// t.Setenv values. An empty UPMO_ENV is production, like an unset one.
func TestLoadRefusesDevelopmentValuesInProduction(t *testing.T) {
	base := func(t *testing.T) {
		t.Helper()
		t.Setenv("UPMO_ENV", "")
		t.Setenv("UPMO_DATABASE_URL", freshDBURL(t))
		t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	}
	for _, tc := range []struct {
		name, key, value, want string
	}{
		{"master key", "UPMO_MASTER_KEY", composeMasterKey(), "UPMO_MASTER_KEY uses a development or weak key"},
		{"service key", "UPMO_SERVICE_KEY", composeServiceKey(), "UPMO_SERVICE_KEY uses a development or weak key"},
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
	t.Run("database url", func(t *testing.T) {
		base(t)
		t.Setenv("UPMO_DATABASE_URL", composeDBURL())
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "UPMO_DATABASE_URL uses a development or empty password") {
			t.Fatalf("Load err = %v", err)
		}
		if strings.Contains(err.Error(), "upmo:upmo") {
			t.Fatalf("Load err holds the credentials: %v", err)
		}
	})
	t.Run("missing master key", func(t *testing.T) {
		base(t)
		t.Setenv("UPMO_DATABASE_APP_URL", freshDBURL(t))
		_, err := LoadBrain()
		if err == nil || !strings.Contains(err.Error(), "UPMO_MASTER_KEY is required in production") {
			t.Fatalf("LoadBrain err = %v", err)
		}
	})
}

func TestLoadAcceptsFreshValuesInProduction(t *testing.T) {
	t.Setenv("UPMO_ENV", "")
	t.Setenv("UPMO_DATABASE_URL", freshDBURL(t))
	t.Setenv("UPMO_DATABASE_APP_URL", freshDBURL(t))
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("UPMO_MASTER_KEY", freshKey(t))
	t.Setenv("UPMO_SERVICE_KEY", "upmo_svc_"+freshKey(t))
	t.Setenv("UPMO_CREDENTIAL_house", "sk-real-provider-key")
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := LoadBrain(); err != nil {
		t.Fatalf("LoadBrain: %v", err)
	}
}

func TestLoadDevelopmentRule(t *testing.T) {
	t.Setenv("UPMO_ENV", "development")
	t.Setenv("UPMO_DATABASE_URL", composeDBURL())
	t.Setenv("UPMO_DATABASE_APP_URL", "postgres://upmo_app:upmo_app@postgres:5432/upmo?sslmode=disable")
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("UPMO_MASTER_KEY", composeMasterKey())
	t.Setenv("UPMO_SERVICE_KEY", composeServiceKey())
	t.Setenv("UPMO_CREDENTIAL_house", composeHouseValue())
	if _, err := LoadBrain(); err != nil {
		t.Fatalf("LoadBrain with the compose values in development: %v", err)
	}
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

func TestLoadSandboxdGuardsTheServiceKey(t *testing.T) {
	t.Setenv("UPMO_ENV", "")
	t.Setenv("UPMO_SERVICE_KEY", composeServiceKey())
	_, err := LoadSandboxd()
	if err == nil || !strings.Contains(err.Error(), "UPMO_SERVICE_KEY uses a development or weak key") {
		t.Fatalf("LoadSandboxd err = %v", err)
	}
	if strings.Contains(err.Error(), composeServiceKey()) {
		t.Fatalf("LoadSandboxd err holds the key: %v", err)
	}

	t.Setenv("UPMO_ENV", "development")
	if _, err := LoadSandboxd(); err != nil {
		t.Fatalf("LoadSandboxd in development: %v", err)
	}

	t.Setenv("UPMO_ENV", "")
	t.Setenv("UPMO_SERVICE_KEY", "upmo_svc_"+freshKey(t))
	if _, err := LoadSandboxd(); err != nil {
		t.Fatalf("LoadSandboxd with a fresh key: %v", err)
	}
}
