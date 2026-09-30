package gateway

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var credentialRef = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// ErrMissing is a credential the backend does not hold.
var ErrMissing = errors.New("gateway: missing credential")

// EnvSecrets reads UPMO_CREDENTIAL_<ref>. The value is never logged.
type EnvSecrets struct{}

func (EnvSecrets) Get(ref string) (string, error) {
	if !credentialRef.MatchString(ref) {
		return "", fmt.Errorf("gateway: credential ref")
	}
	v, ok := os.LookupEnv("UPMO_CREDENTIAL_" + ref)
	if !ok || v == "" {
		return "", ErrMissing
	}
	return v, nil
}

// AWSSecrets resolves a ref through an injected getter.
// A nil Getter means every lookup is missing.
type AWSSecrets struct {
	Getter func(ref string) (string, error)
}

func (a AWSSecrets) Get(ref string) (string, error) {
	if !credentialRef.MatchString(ref) {
		return "", fmt.Errorf("gateway: credential ref")
	}
	if a.Getter == nil {
		return "", ErrMissing
	}
	v, err := a.Getter(ref)
	if err != nil || strings.TrimSpace(v) == "" {
		return "", ErrMissing
	}
	return v, nil
}

// VaultSecrets reads a KV path through an injected getter.
// Addr and Token must be set or every lookup is missing.
type VaultSecrets struct {
	Addr  string
	Token string
	Path  string
	Fetch func(addr, token, path, ref string) (string, error)
}

func (v VaultSecrets) Get(ref string) (string, error) {
	if !credentialRef.MatchString(ref) {
		return "", fmt.Errorf("gateway: credential ref")
	}
	if strings.TrimSpace(v.Addr) == "" || strings.TrimSpace(v.Token) == "" || v.Fetch == nil {
		return "", ErrMissing
	}
	secret, err := v.Fetch(v.Addr, v.Token, v.Path, ref)
	if err != nil || strings.TrimSpace(secret) == "" {
		return "", ErrMissing
	}
	return secret, nil
}

// SecretBackend is one enabled secret source.
type SecretBackend interface {
	Get(ref string) (string, error)
}

// Chain tries each enabled backend until one returns a value.
type Chain []SecretBackend

func (c Chain) Get(ref string) (string, error) {
	if !credentialRef.MatchString(ref) {
		return "", fmt.Errorf("gateway: credential ref")
	}
	for _, backend := range c {
		if backend == nil {
			continue
		}
		v, err := backend.Get(ref)
		if err == nil && v != "" {
			return v, nil
		}
	}
	return "", ErrMissing
}
