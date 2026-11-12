package domain

import "testing"

func house() Account {
	return Account{ID: "h", Adapter: "fake", CredentialRef: "house", PlanKind: PlanMetered, House: true}
}

func byok() Account {
	return Account{ID: "b", Adapter: "fake", CredentialRef: "customer", PlanKind: PlanMetered, House: false}
}

func TestRouteCheck(t *testing.T) {
	ok := Route{
		Name:       "memory-check",
		Capability: CapDecide,
		Decision:   DecisionYesNo,
		Links:      []Link{{Position: 1, Account: house(), Model: "small", RetryBudget: 1}},
	}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Decision = DecisionNone
	if err := bad.Check(); err == nil {
		t.Fatal("decide without decision")
	}
	chat := Route{Name: "chat", Capability: CapChat, Links: ok.Links}
	if err := chat.Check(); err != nil {
		t.Fatal(err)
	}
	chat.Decision = DecisionYesNo
	if err := chat.Check(); err == nil {
		t.Fatal("chat with decision")
	}
}

func TestBringYourOwnKeyNeverFallsBackToHouse(t *testing.T) {
	r := Route{
		Name:       "memory-check",
		Capability: CapDecide,
		Decision:   DecisionYesNo,
		Links: []Link{
			{Position: 1, Account: byok(), Model: "small", RetryBudget: 1},
			{Position: 2, Account: house(), Model: "small", RetryBudget: 1},
		},
	}
	got, err := r.LinksFor("customer")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Account.House || got[0].Account.CredentialRef != "customer" {
		t.Fatalf("byok chain = %+v", got)
	}
	houseOnly, err := r.LinksFor("")
	if err != nil {
		t.Fatal(err)
	}
	if len(houseOnly) != 1 || !houseOnly[0].Account.House {
		t.Fatalf("house chain = %+v", houseOnly)
	}
	if _, err := r.LinksFor("other"); err == nil {
		t.Fatal("unknown ref")
	}
}

func TestLinksForUserHasTheirOwnAndTheWorkspaceAndNeverAnotherPersons(t *testing.T) {
	mine := Account{ID: "1", Adapter: "a", CredentialRef: "personal", OwnerUserID: "u1", Visibility: VisibilityPrivate}
	theirs := Account{ID: "3", Adapter: "a", CredentialRef: "other", OwnerUserID: "u2", Visibility: VisibilityPrivate}
	r := Route{Name: "chat", Capability: CapChat, Links: []Link{
		{Position: 1, Account: mine, Model: "mine"},
		{Position: 2, Account: theirs, Model: "theirs"},
		{Position: 3, Account: house(), Model: "house"},
	}}
	got, err := r.LinksForUser("u1", nil)
	if err != nil || len(got) != 2 || got[0].Model != "mine" || got[1].Model != "house" {
		t.Fatalf("own chain = %+v %v", got, err)
	}
	got, err = r.LinksForUser("u3", nil)
	if err != nil || len(got) != 1 || got[0].Model != "house" {
		t.Fatalf("house chain = %+v %v", got, err)
	}
	onlyMine := Route{Name: "chat", Capability: CapChat, Links: r.Links[:1]}
	if _, err := onlyMine.LinksForUser("u3", nil); err == nil {
		t.Fatal("another person was given a personal link")
	}
}
