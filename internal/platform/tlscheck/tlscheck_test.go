package tlscheck

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExemptHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"localhost.", true},
		{"127.0.0.1", true},
		{"127.0.0.1.", true},
		{"::1", true},
		{"[::1]", true},
		{"postgres", true},
		{"gateway", true},
		{"sandboxd", true},
		{"markitdown", true},
		{"mailpit", true},
		{"vault", true},
		{"db-1", true},
		{"db.example.com", false},
		{"postgres.default.svc", false},
		{"localhost.example.com", false},
		{"127.0.0.2", false},
		{"::1.2", false},
		{"localhost..", false},
		{"2001:db8::1", false},
		{"fe80::1", false},
		{"::ffff:808:808", false},
		{"[2001:db8::1]", false},
		{"post_gres", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := ExemptHost(tc.host); got != tc.want {
			t.Errorf("ExemptHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// Only development on an exempt host may skip TLS. Production never allows
// plaintext, whatever the host.
func TestPlaintextAllowed(t *testing.T) {
	cases := []struct {
		development bool
		host        string
		want        bool
	}{
		{true, "vault", true},
		{true, "localhost", true},
		{true, "127.0.0.1", true},
		{true, "vault.example", false},
		{false, "vault", false},
		{false, "localhost", false},
		{false, "127.0.0.1", false},
		{false, "vault.example", false},
	}
	for _, tc := range cases {
		if got := PlaintextAllowed(tc.development, tc.host); got != tc.want {
			t.Errorf("PlaintextAllowed(development %v, %q) = %v, want %v", tc.development, tc.host, got, tc.want)
		}
	}
}

// TestRootCAs builds a pool that verifies a certificate the CA file signed,
// and answers an empty path, a missing path and a file with no certificate
// with the one error that names the variable.
func TestRootCAs(t *testing.T) {
	caFile, leaf := writeCAWithLeaf(t)
	pool, err := RootCAs(caFile)
	if err != nil {
		t.Fatalf("RootCAs(%q): %v", caFile, err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Fatalf("the pool does not verify a certificate the CA file signed: %v", err)
	}

	plain := filepath.Join(t.TempDir(), "not-a-certificate.pem")
	if err := os.WriteFile(plain, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "tlscheck: UPMO_TLS_CA_FILE must name a readable PEM CA file"
	for name, path := range map[string]string{
		"empty path":   "",
		"missing path": filepath.Join(t.TempDir(), "missing.pem"),
		"non-PEM file": plain,
	} {
		pool, err := RootCAs(path)
		if err == nil || err.Error() != want || pool != nil {
			t.Errorf("%s: RootCAs = pool %v, err %v; want no pool and err %q", name, pool, err, want)
		}
	}
}

// TestPostgresURLTableRefusesThePlantedRequireRow is the rule table for the
// Postgres URL check. The planted bug for this ticket accepts sslmode=require
// on a dotted host: if require is ever accepted, that row fails here.
func TestPostgresURLTableRefusesThePlantedRequireRow(t *testing.T) {
	clearPostgresEnv(t)
	caFile := writeTestCA(t)
	serviceDisable := writeTestServiceFile(t, "disable")
	serviceVerifyFull := writeTestServiceFile(t, "verify-full")
	cases := []struct {
		name    string
		envName string
		url     string
		env     map[string]string
		wantErr string // empty means the URL loads
	}{
		{"verify-full on a dotted host", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo?sslmode=verify-full", nil, ""},
		{"verify-full with a private CA", "UPMO_DATABASE_URL", "postgres://upmo@db.example.com/upmo?sslmode=verify-full&sslrootcert=" + caFile, nil, ""},
		{"verify-full on a non-loopback IPv6 host", "UPMO_DATABASE_URL", "postgres://[2001:db8::1]/upmo?sslmode=verify-full", nil, ""},
		{"disable on a non-loopback IPv6 host", "UPMO_DATABASE_URL", "postgres://[2001:db8::1]/upmo?sslmode=disable", nil, "config: UPMO_DATABASE_URL"},
		{"missing sslmode on a dotted host", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo", nil, "config: UPMO_DATABASE_URL"},
		{"disable on a dotted host", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo?sslmode=disable", nil, "config: UPMO_DATABASE_URL"},
		{"allow on a dotted host", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo?sslmode=allow", nil, "config: UPMO_DATABASE_URL"},
		{"prefer on a dotted host", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo?sslmode=prefer", nil, "config: UPMO_DATABASE_URL"},
		{"planted bug: require on a dotted host", "UPMO_DATABASE_URL", "postgres://db.example/upmo?sslmode=require", nil, "config: UPMO_DATABASE_URL"},
		{"verify-ca on a dotted host", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo?sslmode=verify-ca", nil, "config: UPMO_DATABASE_URL"},
		{"app URL names its own variable", "UPMO_DATABASE_APP_URL", "postgres://db.example.com/upmo?sslmode=require", nil, "config: UPMO_DATABASE_APP_URL"},
		{"disable on a single label", "UPMO_DATABASE_URL", "postgres://postgres:5432/upmo?sslmode=disable", nil, ""},
		{"disable on loopback", "UPMO_DATABASE_URL", "postgres://upmo@127.0.0.1:5432/upmo?sslmode=disable", nil, ""},
		{"no sslmode on localhost", "UPMO_DATABASE_URL", "postgres://upmo@localhost./upmo", nil, ""},
		{"missing sslmode on a keyword string", "UPMO_DATABASE_URL", "host=db.example.com user=upmo", nil, "config: UPMO_DATABASE_URL"},
		{"neither a URL nor a keyword string", "UPMO_DATABASE_URL", "db.example.com", nil, "config: UPMO_DATABASE_URL"},

		{"the host query parameter cannot smuggle a dotted host past loopback", "UPMO_DATABASE_URL", "postgres://127.0.0.1/upmo?sslmode=disable&host=db.example.com", nil, "config: UPMO_DATABASE_URL"},
		{"the host query parameter may stay on one label", "UPMO_DATABASE_URL", "postgres://127.0.0.1/upmo?sslmode=disable&host=postgres", nil, ""},
		{"a comma authority list is every host", "UPMO_DATABASE_URL", "postgres://127.0.0.1,db.example.com/upmo?sslmode=disable", nil, "config: UPMO_DATABASE_URL"},
		{"a comma authority list of exempt hosts loads", "UPMO_DATABASE_URL", "postgres://127.0.0.1,postgres/upmo?sslmode=disable", nil, ""},
		{"a local socket host is allowed in development", "UPMO_DATABASE_URL", "postgres:///upmo?host=/var/run/postgresql&sslmode=disable", nil, ""},
		{"a repeated sslmode must all be verify-full", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo?sslmode=verify-full&sslmode=disable", nil, "config: UPMO_DATABASE_URL"},
		{"a repeated keyword sslmode must all be verify-full", "UPMO_DATABASE_URL", "host=db.example.com sslmode=verify-full sslmode=disable", nil, "config: UPMO_DATABASE_URL"},

		{"verify-full on a dotted host in a keyword DSN", "UPMO_DATABASE_URL", "host=db.example.com sslmode=verify-full", nil, ""},
		{"disable on loopback in a keyword DSN", "UPMO_DATABASE_URL", "host=127.0.0.1 sslmode=disable", nil, ""},
		{"disable on a dotted host in a keyword DSN", "UPMO_DATABASE_URL", "host=db.example.com sslmode=disable", nil, "config: UPMO_DATABASE_URL"},
		{"a quoted keyword host", "UPMO_DATABASE_URL", "host='db.example.com' sslmode=disable", nil, "config: UPMO_DATABASE_URL"},
		{"a comma list in a keyword DSN", "UPMO_DATABASE_URL", "host=127.0.0.1,db.example.com sslmode=disable", nil, "config: UPMO_DATABASE_URL"},
		{"whitespace after = is not an empty host", "UPMO_DATABASE_URL", "host= db.example.com dbname=upmo", nil, "config: UPMO_DATABASE_URL"},
		{"whitespace after = before a quoted host", "UPMO_DATABASE_URL", "host= 'db.example.com' user=upmo", nil, "config: UPMO_DATABASE_URL"},

		{"PGHOST names the host when the string does not", "UPMO_DATABASE_URL", "postgres:///upmo", map[string]string{"PGHOST": "db.example.com"}, "config: UPMO_DATABASE_URL"},
		{"PGHOST on loopback", "UPMO_DATABASE_URL", "postgres:///upmo", map[string]string{"PGHOST": "127.0.0.1"}, ""},
		{"PGHOST is a comma list", "UPMO_DATABASE_URL", "postgres:///upmo", map[string]string{"PGHOST": "127.0.0.1,db.example.com"}, "config: UPMO_DATABASE_URL"},
		{"PGSSLMODE verify-full satisfies a dotted host", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo", map[string]string{"PGSSLMODE": "verify-full"}, ""},
		{"PGSSLMODE disable on a dotted host", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo", map[string]string{"PGSSLMODE": "disable"}, "config: UPMO_DATABASE_URL"},
		{"the sslmode in the string wins over PGSSLMODE", "UPMO_DATABASE_URL", "postgres://db.example.com/upmo?sslmode=verify-full", map[string]string{"PGSSLMODE": "disable"}, ""},
		{"the host in the string wins over PGHOST", "UPMO_DATABASE_URL", "postgres://127.0.0.1/upmo?sslmode=disable", map[string]string{"PGHOST": "db.example.com"}, ""},

		{"a service file may not move the dial to a dotted host in plaintext", "UPMO_DATABASE_URL", "postgres:///upmo?service=prod", map[string]string{"PGSERVICEFILE": serviceDisable}, "config: UPMO_DATABASE_URL"},
		{"a service file with sslmode=verify-full loads", "UPMO_DATABASE_URL", "postgres:///upmo?service=prod", map[string]string{"PGSERVICEFILE": serviceVerifyFull}, ""},
		{"PGSERVICE names the service when the string does not", "UPMO_DATABASE_URL", "postgres:///upmo", map[string]string{"PGSERVICE": "prod", "PGSERVICEFILE": serviceDisable}, "config: UPMO_DATABASE_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			err := PostgresURL(tc.envName, tc.url, true)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to say %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), tc.url) {
				t.Fatalf("err names the URL: %v", err)
			}
		})
	}
}

// TestPostgresURLProductionNeedsVerifyFullAndClientCertificates is the
// production rule: one DNS label is no longer exempt, and verify-full needs
// the database CA and a client certificate and key. The same single-label
// disable URL still loads in development.
func TestPostgresURLProductionNeedsVerifyFullAndClientCertificates(t *testing.T) {
	clearPostgresEnv(t)
	caFile := writeTestCA(t)
	certFile, keyFile := writeTestClientCert(t)
	full := "postgres://upmo@postgres:5432/upmo?sslmode=verify-full&sslrootcert=" + caFile + "&sslcert=" + certFile + "&sslkey=" + keyFile
	cases := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"disable on one label", "postgres://upmo@postgres:5432/upmo?sslmode=disable", "config: UPMO_DATABASE_URL must use sslmode=verify-full in production"},
		{"disable on loopback", "postgres://upmo@127.0.0.1:5432/upmo?sslmode=disable", "config: UPMO_DATABASE_URL must use sslmode=verify-full in production"},
		{"require on one label", "postgres://upmo@postgres:5432/upmo?sslmode=require", "config: UPMO_DATABASE_URL must use sslmode=verify-full in production"},
		{"verify-ca on one label", "postgres://upmo@postgres:5432/upmo?sslmode=verify-ca&sslrootcert=" + caFile, "config: UPMO_DATABASE_URL must use sslmode=verify-full in production"},
		{"verify-full without sslrootcert", "postgres://upmo@postgres:5432/upmo?sslmode=verify-full", "config: UPMO_DATABASE_URL must name the database CA in sslrootcert in production"},
		{"verify-full without a client certificate", "postgres://upmo@postgres:5432/upmo?sslmode=verify-full&sslrootcert=" + caFile, "config: UPMO_DATABASE_URL must name a client certificate in sslcert and sslkey in production"},
		{"verify-full with the CA and a client certificate", full, ""},
		{"a Unix socket host", "postgres://upmo@/upmo?host=/var/run/postgresql", "config: UPMO_DATABASE_URL must not use a Unix socket in production"},
		{"a Unix socket host with verify-full and certificates", "postgres://upmo@/upmo?host=/var/run/postgresql&sslmode=verify-full&sslrootcert=" + caFile + "&sslcert=" + certFile + "&sslkey=" + keyFile, "config: UPMO_DATABASE_URL must not use a Unix socket in production"},
		{"a Unix socket as a fallback host", "host=db.example.com,/var/run/postgresql user=upmo sslmode=verify-full sslrootcert=" + caFile + " sslcert=" + certFile + " sslkey=" + keyFile, "config: UPMO_DATABASE_URL must not use a Unix socket in production"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := PostgresURL("UPMO_DATABASE_URL", tc.url, false)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
	if err := PostgresURL("UPMO_DATABASE_URL", "postgres://upmo@postgres:5432/upmo?sslmode=disable", true); err != nil {
		t.Fatalf("development single-label disable = %v, want none", err)
	}
}

// writeTestClientCert writes a throwaway self-signed client certificate and
// its key and returns both paths: pgconn loads them while parsing.
func writeTestClientCert(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "upmo"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// clearPostgresEnv unsets every PG* variable pgconn.ParseConfig reads, so a
// row sees only the environment it sets: ParseConfig, not the caller, reads
// the process environment now.
func clearPostgresEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PGAPPNAME", "PGCHANNELBINDING", "PGCONNECT_TIMEOUT", "PGDATABASE",
		"PGHOST", "PGMAXPROTOCOLVERSION", "PGMINPROTOCOLVERSION", "PGOPTIONS",
		"PGPASSFILE", "PGPASSWORD", "PGPORT", "PGREQUIREAUTH", "PGSERVICE",
		"PGSERVICEFILE", "PGSSLCERT", "PGSSLCERTMODE", "PGSSLKEY",
		"PGSSLMODE", "PGSSLNEGOTIATION", "PGSSLPASSWORD", "PGSSLROOTCERT",
		"PGSSLSNI", "PGTARGETSESSIONATTRS", "PGTZ", "PGUSER",
	} {
		value, present := os.LookupEnv(name)
		if !present {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Setenv(name, value); err != nil {
				t.Errorf("restore %s: %v", name, err)
			}
		})
	}
}

// writeTestCA writes a throwaway self-signed CA certificate and returns its
// path: pgconn reads the sslrootcert file while parsing, so a row naming a
// private CA needs one that exists.
func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "upmo-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeCAWithLeaf writes a throwaway CA certificate and returns its path and
// a server certificate the CA signed, so a test can check what a pool built
// from the file verifies.
func writeCAWithLeaf(t *testing.T) (string, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "upmo-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	file := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		DNSNames:     []string{"vault"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	signed, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(signed)
	if err != nil {
		t.Fatal(err)
	}
	return path, parsed
}

// writeTestServiceFile writes a Postgres service file whose prod service
// names the dotted host db.example.com with the given sslmode, and returns
// its path.
func writeTestServiceFile(t *testing.T, sslmode string) string {
	t.Helper()
	content := "[prod]\nhost=db.example.com\nuser=upmo\nsslmode=" + sslmode + "\n"
	path := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
