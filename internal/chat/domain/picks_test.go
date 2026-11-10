package domain

import (
	"errors"
	"strings"
	"testing"
)

const pinAccount = "018f3b2c-7a10-7c4a-8e11-111111111111"

func TestPinNeedsBothOrNeither(t *testing.T) {
	for _, tc := range []struct {
		name, account, model string
		ok                   bool
	}{
		{"neither", "", "", true},
		{"both", pinAccount, "gpt-x", true},
		{"account only", pinAccount, "", false},
		{"model only", "", "gpt-x", false},
		{"blank model", pinAccount, "   ", false},
		{"not a uuid", "mine", "gpt-x", false},
		{"control character", pinAccount, "gpt\nx", false},
		{"longest model", pinAccount, strings.Repeat("m", PinModelRunes), true},
		{"model too long", pinAccount, strings.Repeat("m", PinModelRunes+1), false},
		{"multibyte model counts runes", pinAccount, strings.Repeat("é", PinModelRunes), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, model, err := Pin(tc.account, tc.model)
			if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrPin)) {
				t.Fatalf("Pin(%q, %q) error = %v, want ok=%v", tc.account, tc.model, err, tc.ok)
			}
			if err != nil && (account != "" || model != "") {
				t.Fatalf("a refused pin returned %q %q", account, model)
			}
		})
	}
}

func TestPinReturnsTheCanonicalAccountAndTrimsTheModel(t *testing.T) {
	account, model, err := Pin(" 018F3B2C-7A10-7C4A-8E11-111111111111 ", " gpt-x ")
	if err != nil || account != pinAccount || model != "gpt-x" {
		t.Fatalf("got %q %q %v", account, model, err)
	}
}

func TestAgentIsEmptyOrAUUID(t *testing.T) {
	if got, err := Agent(""); err != nil || got != "" {
		t.Fatalf("empty agent = %q %v", got, err)
	}
	if got, err := Agent(pinAccount); err != nil || got != pinAccount {
		t.Fatalf("agent = %q %v", got, err)
	}
	if _, err := Agent("research"); !errors.Is(err, ErrAgent) {
		t.Fatalf("agent error = %v", err)
	}
}
