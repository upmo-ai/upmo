package gateway

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
