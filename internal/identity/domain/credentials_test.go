package domain_test

import (
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestCredentialsRemovalRules(t *testing.T) {
	tests := []struct {
		name            string
		initial         domain.Credentials
		removePasskey   error
		removePassword  error
		wantPasskeys    int
		wantHasPassword bool
	}{
		{
			name:            "single passkey without password cannot be removed",
			initial:         domain.Credentials{Passkeys: 1, HasPassword: false},
			removePasskey:   domain.ErrLastPasskey,
			removePassword:  domain.ErrLastPasskey,
			wantPasskeys:    1,
			wantHasPassword: false,
		},
		{
			name:            "single passkey with password can be removed",
			initial:         domain.Credentials{Passkeys: 1, HasPassword: true},
			removePasskey:   nil,
			removePassword:  nil,
			wantPasskeys:    0,
			wantHasPassword: true,
		},
		{
			name:            "password with multiple passkeys can be removed",
			initial:         domain.Credentials{Passkeys: 2, HasPassword: true},
			removePasskey:   nil,
			removePassword:  nil,
			wantPasskeys:    1,
			wantHasPassword: true,
		},
		{
			name:            "two passkeys without password allow removing one",
			initial:         domain.Credentials{Passkeys: 2, HasPassword: false},
			removePasskey:   nil,
			removePassword:  domain.ErrLastPasskey,
			wantPasskeys:    1,
			wantHasPassword: false,
		},
		{
			name:            "password only cannot be removed",
			initial:         domain.Credentials{Passkeys: 0, HasPassword: true},
			removePasskey:   domain.ErrLastPasskey,
			removePassword:  domain.ErrLastPasskey,
			wantPasskeys:    0,
			wantHasPassword: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			passkeyCreds := tc.initial
			err := passkeyCreds.RemovePasskey()
			if !errors.Is(err, tc.removePasskey) {
				t.Fatalf("RemovePasskey() err = %v, want %v", err, tc.removePasskey)
			}
			if err == nil && passkeyCreds.Passkeys != tc.wantPasskeys {
				t.Fatalf("Passkeys = %d, want %d", passkeyCreds.Passkeys, tc.wantPasskeys)
			}

			pwdCreds := tc.initial
			err = pwdCreds.RemovePassword()
			if !errors.Is(err, tc.removePassword) {
				t.Fatalf("RemovePassword() err = %v, want %v", err, tc.removePassword)
			}
			if err == nil && pwdCreds.HasPassword != false {
				t.Fatalf("HasPassword = %v, want false", pwdCreds.HasPassword)
			}
		})
	}
}

func TestCredentialsAddAndSet(t *testing.T) {
	var creds domain.Credentials
	creds.AddPasskey()
	if creds.Passkeys != 1 {
		t.Fatalf("Passkeys = %d, want 1", creds.Passkeys)
	}
	creds.SetPassword()
	if !creds.HasPassword {
		t.Fatal("HasPassword = false, want true")
	}
}
