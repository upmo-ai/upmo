package domain

import (
	"errors"
	"strings"
	"testing"
)

const pinTestAccount = "018f3b2c-7a10-7c4a-8e11-111111111111"

func TestPinCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		pin  Pin
		ok   bool
	}{
		{"no pin", Pin{}, true},
		{"account and model", Pin{AccountID: pinTestAccount, Model: "gpt-x"}, true},
		{"account only", Pin{AccountID: pinTestAccount}, false},
		{"model only", Pin{Model: "gpt-x"}, false},
		{"not a uuid", Pin{AccountID: "mine", Model: "gpt-x"}, false},
		{"padded model", Pin{AccountID: pinTestAccount, Model: " gpt-x"}, false},
		{"control character", Pin{AccountID: pinTestAccount, Model: "gpt\tx"}, false},
		{"model too long", Pin{AccountID: pinTestAccount, Model: strings.Repeat("m", MaxModelName+1)}, false},
		{"provider and model", Pin{Provider: "OpenAI", Model: "gpt-x"}, true},
		{"provider only", Pin{Provider: "OpenAI"}, false},
		{"account and provider", Pin{AccountID: pinTestAccount, Provider: "OpenAI", Model: "gpt-x"}, false},
		{"padded provider", Pin{Provider: " OpenAI", Model: "gpt-x"}, false},
		{"provider with a control character", Pin{Provider: "Open\nAI", Model: "gpt-x"}, false},
		{"provider too long", Pin{Provider: strings.Repeat("p", MaxProviderLabel+1), Model: "gpt-x"}, false},
		{"provider with a padded model", Pin{Provider: "OpenAI", Model: "gpt-x "}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.pin.Check()
			var invalid Invalid
			if tc.ok != (err == nil) || (!tc.ok && !errors.As(err, &invalid)) {
				t.Fatalf("Check() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestPinnedGoesFirstAndLeavesNoDuplicate(t *testing.T) {
	a := Account{ID: "a"}
	b := Account{ID: "b"}
	chain := []Link{
		{Position: 1, Account: a, Model: "m1"},
		{Position: 2, Account: b, Model: "m2"},
		{Position: 3, Account: a, Model: "m3"},
	}
	got := Pinned(Link{Position: 2, Account: b, Model: "m2", RetryBudget: 5}, chain)
	var models []string
	for _, l := range got {
		models = append(models, l.Model)
	}
	if strings.Join(models, ",") != "m2,m1,m3" || got[0].RetryBudget != 5 {
		t.Fatalf("chain = %v first = %+v", models, got[0])
	}

	// The same model on another account is a different link and stays.
	got = Pinned(Link{Position: 1, Account: b, Model: "m1"}, chain)
	if len(got) != 4 {
		t.Fatalf("a pin to another account's m1 left %d links, want 4", len(got))
	}
	if len(chain) != 3 {
		t.Fatal("Pinned changed the chain it was given")
	}
}

func TestPinLinkTakesTheRoutesBudgetOrTheDefault(t *testing.T) {
	a := Account{ID: "a"}
	b := Account{ID: "b"}
	route := Route{Links: []Link{{Position: 4, Account: a, Model: "m1", RetryBudget: 0, Weight: 3, Need: []string{"vision"}}}}

	on := route.PinLink(a, "m1")
	if on.RetryBudget != 0 || on.Position != PinPosition || len(on.Need) != 1 {
		t.Fatalf("pin on the route = %+v", on)
	}
	off := route.PinLink(b, "m1")
	if off.RetryBudget != DefaultPinRetries || off.Position != PinPosition || len(off.Need) != 0 || off.Account.ID != "b" {
		t.Fatalf("pin off the route = %+v", off)
	}
}

func TestAPinnedLinkIsAlwaysAtPositionZeroAndRouteLinksKeepTheirs(t *testing.T) {
	a := Account{ID: "a"}
	route := Route{Links: []Link{{Position: 3, Account: a, Model: "m1"}, {Position: 4, Account: Account{ID: "b"}, Model: "m2"}}}
	for _, pinned := range []Link{route.PinLink(a, "m1"), route.PinLink(Account{ID: "c"}, "other")} {
		got := Pinned(pinned, route.Links)
		if got[0].Position != 0 {
			t.Fatalf("pinned link at position %d, want 0", got[0].Position)
		}
		for _, l := range got[1:] {
			if want := map[string]int{"m1": 3, "m2": 4}[l.Model]; l.Position != want {
				t.Fatalf("route link %s moved to position %d, want %d", l.Model, l.Position, want)
			}
		}
	}
	if PinPosition != 0 {
		t.Fatalf("PinPosition = %d", PinPosition)
	}
}

func TestOwnLinksAreTheOwnersAloneAndMayBeNone(t *testing.T) {
	route := Route{Links: []Link{
		{Position: 1, Account: Account{ID: "h", House: true}, Model: "mh"},
		{Position: 2, Account: Account{ID: "x", OwnerUserID: "u2", Visibility: VisibilityPrivate}, Model: "mx"},
		{Position: 3, Account: Account{ID: "o", OwnerUserID: "u1", Visibility: VisibilityPrivate}, Model: "mo"},
		// Owned, but shared: it is not a private key, so a pin to a private
		// account does not fall back through it.
		{Position: 4, Account: Account{ID: "s", OwnerUserID: "u1", Visibility: VisibilityTeam, TeamIDs: []string{"t1"}}, Model: "ms"},
		// Disabled own private account: omitted from fallback.
		{Position: 5, Account: Account{ID: "d", OwnerUserID: "u1", Visibility: VisibilityPrivate, Disabled: true}, Model: "md"},
	}}
	if got := route.OwnLinks("u1"); len(got) != 1 || got[0].Model != "mo" {
		t.Fatalf("own links = %+v", got)
	}
	if got := route.OwnLinks("u3"); len(got) != 0 {
		t.Fatalf("a person with no links of their own got %+v", got)
	}
	if got := route.OwnLinks(""); len(got) != 0 {
		t.Fatalf("no person got %+v", got)
	}
}

func TestPinUsableIsOwnTeamOrWorkspaceNeverSomeoneElses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account Account
		ok      bool
	}{
		{"own", Account{OwnerUserID: "u1", Visibility: VisibilityPrivate}, true},
		{"house", Account{House: true}, true},
		{"everyone", Account{OwnerUserID: "u9", Visibility: VisibilityEveryone}, true},
		{"another person's", Account{OwnerUserID: "u2", Visibility: VisibilityPrivate}, false},
		{"shared with a team of theirs", Account{OwnerUserID: "u9", Visibility: VisibilityTeam, TeamIDs: []string{"t1", "t9"}}, true},
		{"shared only with other teams", Account{OwnerUserID: "u1", Visibility: VisibilityTeam, TeamIDs: []string{"t8", "t9"}}, false},
	} {
		if got := PinUsable(tc.account, "u1", []string{"t1", "t2"}); got != tc.ok {
			t.Errorf("%s: PinUsable = %v, want %v", tc.name, got, tc.ok)
		}
	}
}
