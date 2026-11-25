// Package tlscheck decides which targets may be reached without TLS: loopback
// hosts and single DNS labels (the Compose service names). Every other host
// refuses to load unless the connection is TLS with a verified certificate.
// No setting turns this off.
package tlscheck

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// errCAFile is the one refusal every missing or unusable UPMO_TLS_CA_FILE
// becomes, so a dial that needs a CA fails closed with a name the operator
// can act on.
var errCAFile = errors.New("tlscheck: UPMO_TLS_CA_FILE must name a readable PEM CA file")

// dnsLabel is one DNS label: letters, digits and hyphens, nothing else. A
// colon (any IPv6 literal) or a dot never matches.
var dnsLabel = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ExemptHost reports whether host may be reached in plaintext: loopback
// (localhost, 127.0.0.1, ::1, in brackets or with a trailing dot) or one DNS
// label. Anything else — a dotted name, another IPv4 address, any non-loopback
// IPv6 literal — requires TLS, postgres.default.svc included.
func ExemptHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return dnsLabel.MatchString(h)
}

// PlaintextAllowed reports whether host may be reached without TLS in the
// given environment: development keeps the loopback and single-label
// exemption. Production never allows plaintext, not even on one label.
func PlaintextAllowed(development bool, host string) bool {
	return development && ExemptHost(host)
}

// RootCAs returns a new pool holding only the PEM certificates in path, never
// the system roots. An empty path, an unreadable file or a file with no PEM
// certificate fails closed.
func RootCAs(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, errCAFile
	}
	pem, err := os.ReadFile(path) // #nosec G304 -- path is UPMO_TLS_CA_FILE, the operator's own CA bundle
	if err != nil {
		return nil, errCAFile
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errCAFile
	}
	return pool, nil
}

// PostgresURL refuses a Postgres connection string unless every target pgx
// would dial is exempt, or dialled with a verified certificate. pgx itself
// parses the string — keyword DSN quirks, the host= parameter, comma lists,
// the PG* environment and the service file — so the hosts checked are the
// hosts pgx resolves at connect time, and a string pgx cannot parse (or a
// private CA it cannot read) fails closed. The dial targets are the main
// config and each fallback; a host that starts with / is a Unix socket
// directory pgx never wraps in TLS. A non-exempt host must land on sslmode
// verify-full, which is exactly the tls.Config with TLS on, the certificate
// verified and the server name checked; require and verify-ca skip the name
// check, prefer and allow keep a plaintext fallback, and disable has no TLS
// at all. envName names the environment variable in the error; a private CA
// rides on the existing sslrootcert parameter.
func PostgresURL(envName, dsn string) error {
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("config: %s must be a Postgres URL", envName)
	}
	if err := checkDialTarget(envName, config.Host, config.TLSConfig); err != nil {
		return err
	}
	for _, fallback := range config.Fallbacks {
		if err := checkDialTarget(envName, fallback.Host, fallback.TLSConfig); err != nil {
			return err
		}
	}
	return nil
}

// checkDialTarget applies the rule to one host pgx may dial: exempt hosts
// (loopback, one DNS label) and Unix socket directories may use any TLS
// state, every other host must carry verify-full's tls.Config.
func checkDialTarget(envName, host string, tlsConfig *tls.Config) error {
	if strings.HasPrefix(host, "/") || ExemptHost(host) {
		return nil
	}
	if tlsConfig == nil || tlsConfig.InsecureSkipVerify || tlsConfig.ServerName != host {
		return fmt.Errorf("config: %s must use sslmode=verify-full unless the host is loopback or one DNS label", envName)
	}
	return nil
}
