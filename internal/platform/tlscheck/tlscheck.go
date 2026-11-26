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
	"net/url"
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
// would dial is allowed. pgx itself parses the string — keyword DSN quirks,
// the host= parameter, comma lists, the PG* environment and the service file —
// so the hosts checked are the hosts pgx resolves at connect time, and a
// string pgx cannot parse (or a CA or key file it cannot read) fails closed.
// The dial targets are the main config and each fallback; a host that starts
// with / is a Unix socket directory pgx never wraps in TLS, so production
// refuses it and development allows it.
//
// In development an exempt host (loopback, one DNS label) may use any TLS
// state. Every other host, and in production every host, must land on
// sslmode verify-full, which is exactly the tls.Config with TLS on, the
// certificate verified and the server name checked; require and verify-ca
// skip the name check, prefer and allow keep a plaintext fallback, and
// disable has no TLS at all. Production also needs the private CA in
// sslrootcert and the client certificate in sslcert and sslkey, so the
// database checks who is connecting as well as the password. envName names
// the environment variable in the error.
func PostgresURL(envName, dsn string, development bool) error {
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("config: %s must be a Postgres URL", envName)
	}
	clientCert := clientCertNamed(dsn)
	if err := checkDialTarget(envName, config.Host, config.TLSConfig, development, clientCert); err != nil {
		return err
	}
	for _, fallback := range config.Fallbacks {
		if err := checkDialTarget(envName, fallback.Host, fallback.TLSConfig, development, clientCert); err != nil {
			return err
		}
	}
	return nil
}

// keywordSetting finds one key=value pair in a keyword DSN.
func keywordSetting(dsn, key string) string {
	m := regexp.MustCompile(`(?:^|\s)` + key + `\s*=\s*('[^']*'|\S+)`).FindStringSubmatch(dsn)
	if m == nil {
		return ""
	}
	return strings.Trim(m[1], "'")
}

// clientCertNamed reports whether the string, or PGSSLCERT and PGSSLKEY,
// name a client certificate and key. pgx loads them while parsing but keeps
// them private, so the names are read here; pgx has already refused a pair
// that does not load. A certificate only a service file names is not seen,
// which refuses it in production rather than accept a missing one.
func clientCertNamed(dsn string) bool {
	var cert, key string
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil {
			cert, key = u.Query().Get("sslcert"), u.Query().Get("sslkey")
		}
	} else {
		cert, key = keywordSetting(dsn, "sslcert"), keywordSetting(dsn, "sslkey")
	}
	if cert == "" {
		cert = os.Getenv("PGSSLCERT")
	}
	if key == "" {
		key = os.Getenv("PGSSLKEY")
	}
	return cert != "" && key != ""
}

// checkDialTarget applies the rule to one host pgx may dial.
func checkDialTarget(envName, host string, tlsConfig *tls.Config, development, clientCert bool) error {
	if strings.HasPrefix(host, "/") {
		// Postgres has no TLS over a Unix socket.
		if development {
			return nil
		}
		return fmt.Errorf("config: %s must not use a Unix socket in production", envName)
	}
	if PlaintextAllowed(development, host) {
		return nil
	}
	if tlsConfig == nil || tlsConfig.InsecureSkipVerify || tlsConfig.ServerName != host {
		if development {
			return fmt.Errorf("config: %s must use sslmode=verify-full unless the host is loopback or one DNS label", envName)
		}
		return fmt.Errorf("config: %s must use sslmode=verify-full in production", envName)
	}
	if development {
		return nil
	}
	if tlsConfig.RootCAs == nil {
		return fmt.Errorf("config: %s must name the database CA in sslrootcert in production", envName)
	}
	if !clientCert {
		return fmt.Errorf("config: %s must name a client certificate in sslcert and sslkey in production", envName)
	}
	return nil
}
