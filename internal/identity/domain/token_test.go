package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTokenScopeNarrows(t *testing.T) {
	held := []Permission{ObjectPermission(TypeTask, ActionView), ObjectPermission(TypeTask, ActionDelete), PermMembersManage}
	scope, err := TokenScope(held, []string{" task:view "})
	if err != nil || len(scope) != 1 || scope[0] != ObjectPermission(TypeTask, ActionView) {
		t.Fatalf("scope = %v %v", scope, err)
	}
	if _, err := TokenScope(held, []string{"task:delete"}); !errors.Is(err, ErrTokenScope) {
		t.Fatalf("delete = %v", err)
	}
	if _, err := TokenScope(held, []string{"members:manage"}); !errors.Is(err, ErrTokenScope) {
		t.Fatalf("install = %v", err)
	}
	if _, err := TokenScope(held, []string{"task:edit"}); !errors.Is(err, ErrTokenScope) {
		t.Fatalf("beyond role = %v", err)
	}
}

func TestAPITokenSecretRoundTrip(t *testing.T) {
	secret, hash, err := NewAPITokenSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, APITokenPrefix) || len(hash) != 32 {
		t.Fatalf("secret %q hash %d", secret, len(hash))
	}
	token, err := ParseAPITokenSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(Hash(token)) != string(hash) {
		t.Fatal("hash mismatch")
	}
	if _, err := ParseAPITokenSecret(strings.TrimPrefix(secret, APITokenPrefix)); !errors.Is(err, ErrToken) {
		t.Fatalf("bare = %v", err)
	}
}

func TestTokenExpiryDate(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	got, err := TokenExpiryDate("2027-01-28", now)
	if err != nil || !got.Equal(time.Date(2027, 1, 28, 23, 59, 59, 0, time.UTC)) {
		t.Fatalf("date = %s %v", got, err)
	}
	if _, err := TokenExpiryDate("2026-10-29", now); !errors.Is(err, ErrTokenDays) {
		t.Fatalf("past = %v", err)
	}
	if _, err := TokenExpiryDate("2027-10-31", now); !errors.Is(err, ErrTokenDays) {
		t.Fatalf("beyond = %v", err)
	}
}

func TestTokenDays(t *testing.T) {
	if days, err := TokenDays(0); err != nil || days != 90 {
		t.Fatalf("default = %d %v", days, err)
	}
	if _, err := TokenDays(366); !errors.Is(err, ErrTokenDays) {
		t.Fatalf("366 = %v", err)
	}
	if _, err := ParseTokenName("  "); !errors.Is(err, ErrTokenName) {
		t.Fatalf("blank = %v", err)
	}
}

func TestTokenAccessNarrows(t *testing.T) {
	subject := joined([]Permission{ObjectPermission(TypeTask, ActionView), ObjectPermission(TypeTask, ActionEdit)})
	subject.Kind = KindToken
	subject.Policy = []Permission{ObjectPermission(TypeTask, ActionView)}
	object := Object{Type: TypeTask, OwnerID: subject.UserID, Visibility: VisibilityPrivate}
	if got := Can(subject, object, ActionView); !got.Allow {
		t.Fatalf("view: %+v", got)
	}
	if got := Can(subject, object, ActionEdit); got.Allow || got.Reason != ReasonScope {
		t.Fatalf("edit: %+v", got)
	}
	if !TokenExpired(time.Now(), time.Now()) {
		t.Fatal("boundary is still live")
	}
}
