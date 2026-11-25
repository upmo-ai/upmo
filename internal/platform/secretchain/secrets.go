// Package secretchain resolves the credentials a provider call needs: each
// enabled backend in turn until one returns a value. The gateway and the
// brain build the same chain from the environment.
package secretchain

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
)

var refPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// ValidRef reports whether ref can name a credential.
func ValidRef(ref string) bool {
	return refPattern.MatchString(ref)
}

// ErrMissing is a credential the backend does not hold.
var ErrMissing = errors.New("secretchain: missing credential")

// EnvSecrets reads UPMO_CREDENTIAL_<ref>. The value is never logged.
type EnvSecrets struct{}

func (EnvSecrets) Get(ref string) (string, error) {
	if !ValidRef(ref) {
		return "", fmt.Errorf("secretchain: credential ref")
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
	if !ValidRef(ref) {
		return "", fmt.Errorf("secretchain: credential ref")
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
	if !ValidRef(ref) {
		return "", fmt.Errorf("secretchain: credential ref")
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

// Backend is one enabled secret source.
type Backend interface {
	Get(ref string) (string, error)
}

// Chain tries each enabled backend until one returns a value.
type Chain []Backend

func (c Chain) Get(ref string) (string, error) {
	if !ValidRef(ref) {
		return "", fmt.Errorf("secretchain: credential ref")
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

// FromEnv builds the standard chain: the environment, then Vault when both
// UPMO_VAULT_ADDR and UPMO_VAULT_TOKEN are set. Production (the default
// UPMO_ENV) requires an https address on any host; development still allows
// http on loopback or one DNS label. The CA file is not read here: every
// fetch loads UPMO_TLS_CA_FILE itself.
func FromEnv(lookup func(string) (string, bool)) (Chain, error) {
	envValue, _ := lookup("UPMO_ENV")
	env, err := config.ParseEnv(envValue)
	if err != nil {
		return nil, err
	}
	development := env == config.EnvDevelopment
	chain := Chain{EnvSecrets{}}
	addr, _ := lookup("UPMO_VAULT_ADDR")
	token, _ := lookup("UPMO_VAULT_TOKEN")
	if addr == "" || token == "" {
		return chain, nil
	}
	u, err := url.Parse(addr)
	if err != nil || (u.Scheme != "https" && !tlscheck.PlaintextAllowed(development, u.Hostname())) {
		if !development {
			return nil, fmt.Errorf("secretchain: UPMO_VAULT_ADDR must be https when UPMO_ENV=production")
		}
		return nil, fmt.Errorf("secretchain: UPMO_VAULT_ADDR must be https unless the host is loopback or one DNS label")
	}
	path, _ := lookup("UPMO_VAULT_PATH")
	caFile, _ := lookup("UPMO_TLS_CA_FILE")
	caFile = strings.TrimSpace(caFile)
	chain = append(chain, VaultSecrets{
		Addr:  addr,
		Token: token,
		Path:  path,
		Fetch: VaultHTTP{Development: development, CAFile: caFile}.Fetch,
	})
	return chain, nil
}
