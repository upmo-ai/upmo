// Package pgtls gives unit tests a Postgres URL that passes the production
// TLS rule, so a test can still reach the password and key checks behind it.
package pgtls

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
	"testing"
	"time"
)

// URL returns postgres://<userinfo>@db.example.com:5432/upmo with
// sslmode=verify-full and a throwaway CA, client certificate and key written
// to t.TempDir: pgconn reads all three while parsing. It never dials.
func URL(t *testing.T, userinfo string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, typ string, der []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "upmo-test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	client := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "upmo"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, client, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	caFile := write("ca.pem", "CERTIFICATE", caDER)
	certFile := write("client.crt", "CERTIFICATE", clientDER)
	keyFile := write("client.key", "EC PRIVATE KEY", keyDER)
	return "postgres://" + userinfo + "@db.example.com:5432/upmo?sslmode=verify-full&sslrootcert=" + caFile + "&sslcert=" + certFile + "&sslkey=" + keyFile
}
