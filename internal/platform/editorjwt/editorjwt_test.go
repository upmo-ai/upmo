package editorjwt

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func keys(t *testing.T) Keys {
	t.Helper()
	k, err := ParseKeys(KeyPrefix+base64.RawStdEncoding.EncodeToString(bytes32('e')), "")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	k := keys(t)
	user := "11111111-1111-1111-1111-111111111111"
	task := "22222222-2222-2222-2222-222222222222"
	session := "33333333-3333-3333-3333-333333333333"
	token, err := k.Sign(user, task, session, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("token = %s", token)
	}
	got, err := k.Verify(token, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.User != user || got.Task != task || got.Session != session {
		t.Fatalf("claims = %+v", got)
	}
}

func TestVerifyRejects(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	k := keys(t)
	token, err := k.Sign("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Verify(token, now.Add(lifetime+time.Minute)); !errors.Is(err, errStale) {
		t.Fatalf("stale = %v", err)
	}
	if _, err := k.Verify("upmo_pat_"+strings.Repeat("A", 43), now); !errors.Is(err, errToken) {
		t.Fatalf("personal = %v", err)
	}
	if _, err := New(nil); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty = %v", err)
	}
	for _, brk := range []string{"\n", "\r"} {
		split := token[:len(token)-10] + brk + token[len(token)-10:]
		if _, err := k.Verify(split, now); !errors.Is(err, errToken) {
			t.Fatalf("line break %q in signature = %v", brk, err)
		}
	}
}

func TestParseKey(t *testing.T) {
	raw := bytes32('a')
	for _, secret := range []string{KeyPrefix + base64.RawStdEncoding.EncodeToString(raw), KeyPrefix + base64.StdEncoding.EncodeToString(raw)} {
		got, err := ParseKey(secret)
		if err != nil || string(got) != string(raw) {
			t.Fatalf("%q = %v %v", secret, got, err)
		}
	}
	for _, secret := range []string{
		"",
		"upmo_svc_" + base64.RawStdEncoding.EncodeToString(raw),
		KeyPrefix + base64.RawStdEncoding.EncodeToString(raw[:31]),
		KeyPrefix + base64.RawStdEncoding.EncodeToString(append(raw, 'b')),
	} {
		if _, err := ParseKey(secret); err == nil || err.Error() != "editorjwt: key is not upmo_edk_ followed by base64 of 32 bytes" {
			t.Fatalf("%q error = %v", secret, err)
		}
	}
	if _, err := ParseKeys("", ""); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key = %v", err)
	}
}
