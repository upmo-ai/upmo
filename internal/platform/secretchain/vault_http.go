package secretchain

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
)

// VaultHTTP reads a KV v2 secret data.value or data for the ref key. The zero
// value is production: the address must be https, the dial goes through
// netguard, and the certificate chain must end at a CA in CAFile
// (UPMO_TLS_CA_FILE), never at a system root. Development keeps plaintext http
// on loopback and one DNS label and, with no CAFile, trusts the system roots.
// A redirect is returned, never followed.
type VaultHTTP struct {
	Development bool   // zero value is production
	CAFile      string // UPMO_TLS_CA_FILE
	// Lookup resolves a host. Nil uses the system resolver. Tests set it.
	Lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Fetch reads the secret for ref from the Vault at addr.
func (v VaultHTTP) Fetch(addr, token, path, ref string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(addr), "/")
	mount := strings.Trim(strings.TrimSpace(path), "/")
	if mount == "" {
		mount = "secret/data"
	}
	u, err := url.Parse(base + "/" + mount + "/" + url.PathEscape(ref))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ErrMissing
	}
	if u.Scheme == "http" && !tlscheck.PlaintextAllowed(v.Development, u.Hostname()) {
		if !v.Development {
			return "", fmt.Errorf("secretchain: UPMO_VAULT_ADDR must be https when UPMO_ENV=production")
		}
		return "", fmt.Errorf("secretchain: UPMO_VAULT_ADDR must be https unless the host is loopback or one DNS label")
	}
	var pool *x509.CertPool
	if !v.Development || v.CAFile != "" {
		pool, err = tlscheck.RootCAs(v.CAFile)
		if err != nil {
			return "", err
		}
	}
	// Vault sits on the private network, so the guard allows private and
	// loopback targets; link-local and metadata addresses stay refused.
	g := &netguard.Guard{LocalNetwork: true, Lookup: v.Lookup}
	t := g.Transport()
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: t,
		// A redirect is a response, not a hop: the token must never leave
		// the address the operator named.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", ErrMissing
	}
	req.Header.Set("X-Vault-Token", token)
	res, err := client.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return "", fmt.Errorf("secretchain: Vault certificate is not trusted by UPMO_TLS_CA_FILE: %w", err)
		}
		return "", fmt.Errorf("secretchain: vault: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil || res.StatusCode != http.StatusOK {
		return "", ErrMissing
	}
	var payload struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", ErrMissing
	}
	if value, ok := payload.Data.Data["value"]; ok {
		return fmt.Sprint(value), nil
	}
	if value, ok := payload.Data.Data[ref]; ok {
		return fmt.Sprint(value), nil
	}
	return "", ErrMissing
}
