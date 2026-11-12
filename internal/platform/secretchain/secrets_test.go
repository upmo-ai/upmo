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
	empty := FromEnv(func(string) (string, bool) { return "", false })
	if len(empty) != 1 {
		t.Fatalf("no vault vars: %d backends", len(empty))
	}
	if _, ok := empty[0].(EnvSecrets); !ok {
		t.Fatalf("first backend = %T", empty[0])
	}
	env := map[string]string{
		"UPMO_VAULT_ADDR":  "https://vault.example",
		"UPMO_VAULT_TOKEN": "t",
		"UPMO_VAULT_PATH":  "secret/data",
	}
	full := FromEnv(func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	})
	if len(full) != 2 {
		t.Fatalf("vault vars: %d backends", len(full))
	}
	if _, ok := full[0].(EnvSecrets); !ok {
		t.Fatalf("first backend = %T", full[0])
	}
	vault, ok := full[1].(VaultSecrets)
	if !ok {
		t.Fatalf("second backend = %T", full[1])
	}
	if vault.Addr != "https://vault.example" || vault.Token != "t" || vault.Path != "secret/data" || vault.Fetch == nil {
		t.Fatalf("vault = %+v", vault)
	}
}
