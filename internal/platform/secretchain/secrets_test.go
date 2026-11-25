package secretchain

import (
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestEnvSecrets(t *testing.T) {
	t.Setenv("UPMO_CREDENTIAL_house", "s")
	s := EnvSecrets{}
	got, err := s.Get("house")
	if err != nil || got != "s" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := s.Get("absent"); !errors.Is(err, ErrMissing) || strings.Contains(err.Error(), "absent") {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := s.Get("sk-live"); err == nil || strings.Contains(err.Error(), "sk-live") {
		t.Fatalf("bad ref err = %v", err)
	}
}

func TestChainTriesBackends(t *testing.T) {
	t.Setenv("UPMO_CREDENTIAL_envonly", "from-env")
	chain := Chain{
		EnvSecrets{},
		AWSSecrets{Getter: func(ref string) (string, error) {
			if ref == "awskey" {
				return "from-aws", nil
			}
			return "", ErrMissing
		}},
		VaultSecrets{
			Addr:  "https://vault.example",
			Token: "t",
			Path:  "secret/data",
			Fetch: func(_, _, _, ref string) (string, error) {
				if ref == "vaultkey" {
					return "from-vault", nil
				}
				return "", ErrMissing
			},
		},
	}
	got, err := chain.Get("envonly")
	if err != nil || got != "from-env" {
		t.Fatalf("env = %q %v", got, err)
	}
	got, err = chain.Get("awskey")
	if err != nil || got != "from-aws" {
		t.Fatalf("aws = %q %v", got, err)
	}
	got, err = chain.Get("vaultkey")
	if err != nil || got != "from-vault" {
		t.Fatalf("vault = %q %v", got, err)
	}
	if _, err := chain.Get("nowhere"); !errors.Is(err, ErrMissing) || strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("missing = %v", err)
	}
	if _, err := chain.Get("bad-ref"); err == nil || strings.Contains(err.Error(), "bad-ref") {
		t.Fatalf("bad ref = %v", err)
	}
}

func TestAWSAndVaultStayQuietWhenDisabled(t *testing.T) {
	if _, err := (AWSSecrets{}).Get("x"); !errors.Is(err, ErrMissing) {
		t.Fatalf("aws = %v", err)
	}
	if _, err := (VaultSecrets{}).Get("x"); !errors.Is(err, ErrMissing) {
		t.Fatalf("vault = %v", err)
	}
}

// envLookup reads names from env; a name it does not hold is unset.
func envLookup(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

func TestFromEnv(t *testing.T) {
	empty, err := FromEnv(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 1 {
		t.Fatalf("no vault vars: %d backends", len(empty))
	}
	if _, ok := empty[0].(EnvSecrets); !ok {
		t.Fatalf("first backend = %T", empty[0])
	}
	// A partial pair still skips Vault without an error.
	for name, env := range map[string]map[string]string{
		"addr only":   {"UPMO_VAULT_ADDR": "http://vault.example"},
		"token only":  {"UPMO_VAULT_TOKEN": "t"},
		"empty token": {"UPMO_VAULT_ADDR": "http://vault.example", "UPMO_VAULT_TOKEN": ""},
	} {
		chain, err := FromEnv(envLookup(env))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(chain) != 1 {
			t.Fatalf("%s: %d backends", name, len(chain))
		}
	}
	// In development a plaintext address off the exempt hosts is refused,
	// IPv6 included. Production (UPMO_ENV unset) refuses plaintext on any
	// host, one DNS label included, and an invalid UPMO_ENV is refused.
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"development, dotted host": {
			map[string]string{"UPMO_ENV": "development", "UPMO_VAULT_ADDR": "http://vault.example", "UPMO_VAULT_TOKEN": "t"},
			"secretchain: UPMO_VAULT_ADDR must be https unless the host is loopback or one DNS label",
		},
		"development, IPv6": {
			map[string]string{"UPMO_ENV": "development", "UPMO_VAULT_ADDR": "http://[2001:db8::1]:8200", "UPMO_VAULT_TOKEN": "t"},
			"secretchain: UPMO_VAULT_ADDR must be https unless the host is loopback or one DNS label",
		},
		"production, one label": {
			map[string]string{"UPMO_VAULT_ADDR": "http://vault", "UPMO_VAULT_TOKEN": "t"},
			"secretchain: UPMO_VAULT_ADDR must be https when UPMO_ENV=production",
		},
		"production, loopback": {
			map[string]string{"UPMO_VAULT_ADDR": "http://127.0.0.1:8200", "UPMO_VAULT_TOKEN": "t"},
			"secretchain: UPMO_VAULT_ADDR must be https when UPMO_ENV=production",
		},
		"production, dotted host": {
			map[string]string{"UPMO_VAULT_ADDR": "http://vault.example", "UPMO_VAULT_TOKEN": "t"},
			"secretchain: UPMO_VAULT_ADDR must be https when UPMO_ENV=production",
		},
		"invalid env": {
			map[string]string{"UPMO_ENV": "staging", "UPMO_VAULT_ADDR": "https://vault.example", "UPMO_VAULT_TOKEN": "t"},
			"config: UPMO_ENV must be production or development",
		},
	} {
		_, err := FromEnv(envLookup(tc.env))
		if err == nil || err.Error() != tc.want {
			t.Fatalf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// https is accepted in production; development also accepts plaintext on
	// the exempt hosts.
	for name, tc := range map[string]struct {
		env  string
		addr string
	}{
		"production https":          {"", "https://vault.example"},
		"development https":         {"development", "https://vault.example"},
		"development loopback":      {"development", "http://127.0.0.1:8200"},
		"development one label":     {"development", "http://vault:8200"},
		"development IPv6 loopback": {"development", "http://[::1]:8200"},
	} {
		env := map[string]string{
			"UPMO_VAULT_ADDR":  tc.addr,
			"UPMO_VAULT_TOKEN": "t",
			"UPMO_VAULT_PATH":  "secret/data",
		}
		if tc.env != "" {
			env["UPMO_ENV"] = tc.env
		}
		chain, err := FromEnv(envLookup(env))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(chain) != 2 {
			t.Fatalf("%s: %d backends", name, len(chain))
		}
		if _, ok := chain[0].(EnvSecrets); !ok {
			t.Fatalf("%s: first backend = %T", name, chain[0])
		}
		vault, ok := chain[1].(VaultSecrets)
		if !ok {
			t.Fatalf("%s: second backend = %T", name, chain[1])
		}
		if vault.Addr != tc.addr || vault.Token != "t" || vault.Path != "secret/data" || vault.Fetch == nil {
			t.Fatalf("%s: vault = %+v", name, vault)
		}
	}
}

// TestFromEnvFetchesThroughTheWiredVault calls Get on the chain FromEnv
// builds, against real listeners, so a field FromEnv leaves out of the Vault
// fetch is a failed lookup and not an unchecked function value.
func TestFromEnvFetchesThroughTheWiredVault(t *testing.T) {
	// The env backend runs first, so keep it empty for this ref.
	t.Setenv("UPMO_CREDENTIAL_house", "")
	// Development with no CA file keeps plaintext http to loopback.
	plain := serveHTTP(t, tls.Certificate{}, KVBody("ok"))
	development := map[string]string{
		"UPMO_ENV":         "development",
		"UPMO_VAULT_ADDR":  fmt.Sprintf("http://127.0.0.1:%d", plain.port()),
		"UPMO_VAULT_TOKEN": "hvs.test",
	}
	chain, err := FromEnv(envLookup(development))
	if err != nil {
		t.Fatal(err)
	}
	got, err := chain.Get("house")
	if err != nil || got != "ok" {
		t.Fatalf("development: got %q %v, want %q", got, err, "ok")
	}

	// Production (UPMO_ENV unset) trusts the CA file for a certificate with
	// the loopback IP as its SAN. The path carries the spaces an operator
	// may leave around it.
	caA := mustCA()
	vault := serveHTTP(t, mustCert(t, caA, "127.0.0.1"), KVBody("ok"))
	production := map[string]string{
		"UPMO_VAULT_ADDR":  fmt.Sprintf("https://127.0.0.1:%d", vault.port()),
		"UPMO_VAULT_TOKEN": "hvs.test",
		"UPMO_TLS_CA_FILE": " " + writeCA(t, caA) + " ",
	}
	chain, err = FromEnv(envLookup(production))
	if err != nil {
		t.Fatal(err)
	}
	got, err = chain.Get("house")
	if err != nil || got != "ok" {
		t.Fatalf("production: got %q %v, want %q", got, err, "ok")
	}

	// Production without the CA file fails closed before any byte is sent.
	refused := serveHTTP(t, mustCert(t, caA, "127.0.0.1"), KVBody("ok"))
	delete(production, "UPMO_TLS_CA_FILE")
	production["UPMO_VAULT_ADDR"] = fmt.Sprintf("https://127.0.0.1:%d", refused.port())
	chain, err = FromEnv(envLookup(production))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chain.Get("house"); !errors.Is(err, ErrMissing) {
		t.Fatalf("production without a CA file: err = %v, want ErrMissing", err)
	}
	if n := refused.accepted(); n != 0 {
		t.Fatalf("vault accepted %d connections", n)
	}
}
