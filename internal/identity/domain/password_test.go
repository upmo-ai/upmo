package domain

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestCheckPassword(t *testing.T) {
	const email, name = "ada@example.com", "Ada Lovelace"
	long := "correct horse battery"
	tests := []struct {
		name, password string
		want           error
	}{
		{"ok", long, nil},
		{"spaces and emoji", "horse battery staple 🐴", nil},
		{"short", "short-password", ErrPasswordShort},
		{"long", strings.Repeat("a", 20) + strings.Repeat("b", 109), ErrPasswordLong},
		{"repetitive", strings.Repeat("ab", 10), ErrPasswordRepetitive},
		{"common", "!2h7=aol64#exq0q", ErrPasswordCommon},
		{"email", "ada@example.com!!!!", ErrPasswordPersonal},
		{"name", "ada lovelace extra", ErrPasswordPersonal},
		{"upmo", "upmo-upmo-upmo-upmo", ErrPasswordPersonal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Check(tt.password, email, name); err != tt.want {
				t.Fatalf("Check(%q) = %v, want %v", tt.password, err, tt.want)
			}
		})
	}
	folded := "ｃｏｒｒｅｃｔ horse battery"
	if Normalize(folded) != long {
		t.Fatalf("Normalize(%q) = %q", folded, Normalize(folded))
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	const password = "correct horse battery"
	phc, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash, err := VerifyPassword(password, phc)
	if err != nil || !ok || rehash {
		t.Fatalf("verify current = %v %v %v", ok, rehash, err)
	}
	ok, _, err = VerifyPassword("correct horse battery!", phc)
	if err != nil || ok {
		t.Fatalf("verify wrong = %v %v", ok, err)
	}
	wide := "ｃｏｒｒｅｃｔ horse battery"
	ok, _, err = VerifyPassword(wide, phc)
	if err != nil || !ok {
		t.Fatalf("verify NFKC = %v %v", ok, err)
	}
}

func TestWeakHashIsReplaced(t *testing.T) {
	const password = "correct horse battery"
	salt := []byte("salt-is-16-bytes")
	key := argon2.IDKey([]byte(password), salt, 1, 8, 1, 16)
	phc := formatPHC(salt, key, 8, 1, 1)
	ok, rehash, err := VerifyPassword(password, phc)
	if err != nil || !ok || !rehash {
		t.Fatalf("weak = %v %v %v", ok, rehash, err)
	}
	ok, _, err = VerifyPassword(password, phc+"\n")
	if err != nil || ok {
		t.Fatalf("newline hash = %v %v", ok, err)
	}
}

func TestOfferPasskeyBoundary(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	if !OfferPasskey(nil, now) {
		t.Fatal("first offer")
	}
	offered := now
	if OfferPasskey(&offered, now.Add(PasskeyOfferAfter-time.Second)) {
		t.Fatal("offered inside the window")
	}
	due := now.Add(PasskeyOfferAfter)
	if !OfferPasskey(&offered, due) {
		t.Fatal("boundary is due")
	}
}

func TestAccountKeyHidesEmail(t *testing.T) {
	key := AccountKey("Ada@Example.com")
	if strings.Contains(strings.ToLower(key), "ada") || !strings.HasPrefix(key, "account:") {
		t.Fatalf("key = %s", key)
	}
	if AccountKey(" ada@example.com ") != key {
		t.Fatal("email is not normalised")
	}
	if !strings.HasPrefix(AccountIPKey("ada@example.com", "198.51.100.9"), "account-ip:") {
		t.Fatal(AccountIPKey("ada@example.com", "198.51.100.9"))
	}
}

func FuzzCheck(f *testing.F) {
	f.Add("correct horse battery", "ada@example.com", "Ada")
	f.Fuzz(func(t *testing.T, password, email, name string) {
		_ = Check(password, email, name)
	})
}

func FuzzParsePHC(f *testing.F) {
	f.Add("$argon2id$v=19$m=8,t=1,p=1$c2FsdA$a2V5")
	f.Add("$argon2id$v=19$m=8,t=1,p=1$c2FsdA\n$a2V5")
	f.Fuzz(func(t *testing.T, s string) {
		_, _, _, _, _, _ = parsePHC(s)
	})
}
