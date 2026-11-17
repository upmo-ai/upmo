package secretchain

import (
	"errors"
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
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			v, ok := env[name]
			return v, ok
		}
	}
	// A partial pair still skips Vault without an error.
	for name, env := range map[string]map[string]string{
		"addr only":   {"UPMO_VAULT_ADDR": "http://vault.example"},
		"token only":  {"UPMO_VAULT_TOKEN": "t"},
		"empty token": {"UPMO_VAULT_ADDR": "http://vault.example", "UPMO_VAULT_TOKEN": ""},
	} {
		chain, err := FromEnv(lookup(env))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(chain) != 1 {
			t.Fatalf("%s: %d backends", name, len(chain))
		}
	}
	// A plaintext address off the exempt hosts is refused, IPv6 included.
	for _, addr := range []string{"http://vault.example", "http://[2001:db8::1]:8200"} {
		_, err = FromEnv(lookup(map[string]string{
			"UPMO_VAULT_ADDR":  addr,
			"UPMO_VAULT_TOKEN": "t",
		}))
		if err == nil || !strings.Contains(err.Error(), "UPMO_VAULT_ADDR") {
			t.Fatalf("%s: err = %v, want it to name UPMO_VAULT_ADDR", addr, err)
		}
	}
	for _, addr := range []string{"https://vault.example", "http://127.0.0.1:8200", "http://vault:8200", "http://[::1]:8200"} {
		chain, err := FromEnv(lookup(map[string]string{
			"UPMO_VAULT_ADDR":  addr,
			"UPMO_VAULT_TOKEN": "t",
			"UPMO_VAULT_PATH":  "secret/data",
		}))
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if len(chain) != 2 {
			t.Fatalf("%s: %d backends", addr, len(chain))
		}
		if _, ok := chain[0].(EnvSecrets); !ok {
			t.Fatalf("%s: first backend = %T", addr, chain[0])
		}
		vault, ok := chain[1].(VaultSecrets)
		if !ok {
			t.Fatalf("%s: second backend = %T", addr, chain[1])
		}
		if vault.Addr != addr || vault.Token != "t" || vault.Path != "secret/data" || vault.Fetch == nil {
			t.Fatalf("%s: vault = %+v", addr, vault)
		}
	}
}
