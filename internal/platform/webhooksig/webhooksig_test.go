package webhooksig

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	stdSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw" // #nosec G101 -- published Standard Webhooks example, not a credential
	stdID     = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	stdBody   = `{"test": 2432232314}`
	stdSig    = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
	stdUnix   = int64(1614265330)

	ghSecret = "It's a Secret to Everybody"
	ghBody   = "Hello, World!"
	ghSig    = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
)

func TestReadBody(t *testing.T) {
	exact := bytes.Repeat([]byte("a"), MaxBody)
	over := bytes.Repeat([]byte("a"), MaxBody+1)
	tests := []struct {
		name    string
		in      []byte
		wantErr bool
	}{
		{"empty", nil, false},
		{"small", []byte("hello"), false},
		{"exact limit", exact, false},
		{"one past the limit", over, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadBody(bytes.NewReader(tt.in))
			if tt.wantErr {
				if !errors.Is(err, errTooLarge) {
					t.Fatalf("ReadBody error = %v, want too large", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.in) {
				t.Fatalf("ReadBody = %d bytes, want %d", len(got), len(tt.in))
			}
		})
	}
}

func TestGitHubReference(t *testing.T) {
	body := []byte(ghBody)
	secret := []byte(ghSecret)
	if got := SignGitHub(secret, body); got != ghSig {
		t.Fatalf("SignGitHub = %s, want %s", got, ghSig)
	}
	if err := VerifyGitHub(secret, body, ghSig); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubFailures(t *testing.T) {
	body := []byte(ghBody)
	secret := []byte(ghSecret)
	tests := []struct {
		name, header string
		secret, body []byte
	}{
		{"wrong secret", ghSig, []byte("other"), body},
		{"bad encoding", "sha256=zz", secret, body},
		{"missing prefix", "757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17", secret, body},
		{"empty header", "", secret, body},
		{"empty secret", ghSig, nil, body},
		{"oversized body", ghSig, secret, bytes.Repeat([]byte("a"), MaxBody+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := VerifyGitHub(tt.secret, tt.body, tt.header); err == nil {
				t.Fatal("VerifyGitHub succeeded")
			}
		})
	}
}

func TestStandardReference(t *testing.T) {
	body := []byte(stdBody)
	when := time.Unix(stdUnix, 0)
	got, err := Sign(stdSecret, stdID, when, body)
	if err != nil {
		t.Fatal(err)
	}
	if got != stdSig {
		t.Fatalf("Sign = %s, want %s", got, stdSig)
	}
	id, err := Verify([]string{stdSecret}, stdID, strconv.FormatInt(stdUnix, 10), stdSig, body, when)
	if err != nil {
		t.Fatal(err)
	}
	if id != stdID {
		t.Fatalf("id = %q, want %q", id, stdID)
	}
}

func TestStandardFailures(t *testing.T) {
	body := []byte(stdBody)
	when := time.Unix(stdUnix, 0)
	ts := strconv.FormatInt(stdUnix, 10)
	other, err := Sign("whsec_"+base64.RawStdEncoding.EncodeToString([]byte("another-secret-key-32-bytes!!")), stdID, when, body)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		id      string
		ts      string
		sig     string
		secrets []string
		body    []byte
		now     time.Time
	}{
		{"wrong secret", stdID, ts, stdSig, []string{"whsec_" + base64.RawStdEncoding.EncodeToString([]byte("not-the-secret"))}, body, when},
		{"bad signature encoding", stdID, ts, "v1,***", []string{stdSecret}, body, when},
		{"missing signature", stdID, ts, "", []string{stdSecret}, body, when},
		{"missing id", "", ts, stdSig, []string{stdSecret}, body, when},
		{"missing timestamp", stdID, "", stdSig, []string{stdSecret}, body, when},
		{"stale past", stdID, ts, stdSig, []string{stdSecret}, body, when.Add(5*time.Minute + time.Second)},
		{"stale future", stdID, ts, stdSig, []string{stdSecret}, body, when.Add(-5*time.Minute - time.Second)},
		{"bad secret", stdID, ts, stdSig, []string{"not-a-secret"}, body, when},
		{"no secret", stdID, ts, stdSig, nil, body, when},
		{"oversized body", stdID, ts, stdSig, []string{stdSecret}, bytes.Repeat([]byte("a"), MaxBody+1), when},
		{"timestamp not a number", stdID, "yesterday", stdSig, []string{stdSecret}, body, when},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Verify(tt.secrets, tt.id, tt.ts, tt.sig, tt.body, tt.now); err == nil {
				t.Fatal("Verify succeeded")
			}
		})
	}
	// The other secret's signature must not verify as the published secret,
	// and a second v1 value is accepted when it is the real one.
	if _, err := Verify([]string{stdSecret}, stdID, ts, other, body, when); err == nil {
		t.Fatal("a signature from another secret verified")
	}
	rotated := other + " " + stdSig
	if _, err := Verify([]string{stdSecret}, stdID, ts, rotated, body, when); err != nil {
		t.Fatalf("key rotation header: %v", err)
	}
	if _, err := Verify([]string{"whsec_" + base64.RawStdEncoding.EncodeToString([]byte("not-the-secret")), stdSecret}, stdID, ts, stdSig, body, when); err != nil {
		t.Fatalf("second secret: %v", err)
	}
}

func TestSignRejects(t *testing.T) {
	when := time.Unix(stdUnix, 0)
	tests := []struct {
		name, secret, id string
		body             []byte
	}{
		{"bad secret", "secret", stdID, []byte(stdBody)},
		{"empty id", stdSecret, "", []byte(stdBody)},
		{"oversized", stdSecret, stdID, bytes.Repeat([]byte("a"), MaxBody+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Sign(tt.secret, tt.id, when, tt.body); err == nil {
				t.Fatal("Sign succeeded")
			}
		})
	}
}

// FuzzVerify checks the header parser and both schemes. The expected MAC is
// computed here, not by Sign, so a bug in Sign cannot hide a forged accept.
func FuzzVerify(f *testing.F) {
	f.Add(stdID, stdUnix, []byte(stdBody), stdSig, ghSig)
	f.Add("", int64(0), []byte(nil), "", "")
	f.Add("id", int64(-1), bytes.Repeat([]byte("a"), 32), "v1,not-base64", "sha256=zz")
	f.Fuzz(func(t *testing.T, id string, unix int64, body []byte, header, ghHeader string) {
		defer func() {
			if recover() != nil {
				t.Fatal("parser panicked")
			}
		}()
		key, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stdSecret, "whsec_"))
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, key)
		content := id + "." + strconv.FormatInt(unix, 10) + "." + string(body)
		_, _ = mac.Write([]byte(content))
		want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
		now := time.Unix(unix, 0)
		_, err = Verify([]string{stdSecret}, id, strconv.FormatInt(unix, 10), header, body, now)
		valid := header == want && id != "" && len(body) <= MaxBody
		if valid && err != nil {
			t.Fatalf("rejected an independently computed signature: %v", err)
		}
		if !valid && err == nil {
			t.Fatalf("verified a forged signature %q", header)
		}

		ghm := hmac.New(sha256.New, []byte(ghSecret))
		_, _ = ghm.Write(body)
		ghWant := "sha256=" + hex.EncodeToString(ghm.Sum(nil))
		err = VerifyGitHub([]byte(ghSecret), body, ghHeader)
		ghValid := ghHeader == ghWant && len(body) <= MaxBody
		if ghValid && err != nil {
			t.Fatalf("rejected an independently computed GitHub signature: %v", err)
		}
		if !ghValid && err == nil {
			t.Fatalf("verified a forged GitHub signature %q", ghHeader)
		}
	})
}
