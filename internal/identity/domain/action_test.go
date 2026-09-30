package domain

import "testing"

func TestDecideActionEmptyRulesMatchConnectorDecide(t *testing.T) {
	allAsk := DefaultConnectorGrant("github")
	theseAllow, err := ParseConnectorGrant("github", "these", []string{"a", "b"}, "allow")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		grant    ConnectorGrant
		action   ConnectorAction
		resource string
	}{
		{allAsk, ConnectorRead, "a"},
		{allAsk, ConnectorChange, "a"},
		{allAsk, ConnectorDelete, "a"},
		{theseAllow, ConnectorRead, "a"},
		{theseAllow, ConnectorChange, "a"},
		{theseAllow, ConnectorDelete, "a"},
		{theseAllow, ConnectorRead, "c"},
		{theseAllow, ConnectorChange, "c"},
		{theseAllow, ConnectorDelete, "c"},
	}
	for _, tc := range cases {
		got := DecideAction(tc.grant, ActionRules{}, tc.action, tc.resource)
		want := ConnectorDecide(tc.grant, tc.action, tc.resource)
		if got != want {
			t.Fatalf("%s %s %s %q = %s, want %s", tc.grant.Scope, tc.grant.Changes, tc.action, tc.resource, got, want)
		}
	}
}

func TestDecideActionDeleteAlwaysAsks(t *testing.T) {
	grant := DefaultConnectorGrant("github")
	for _, policy := range []ChangePolicy{ChangesAllow, ChangesBlock} {
		got := DecideAction(grant, ActionRules{Delete: policy}, ConnectorDelete, "a")
		if got != ConnectorAsk {
			t.Fatalf("delete rule %s = %s, want ask", policy, got)
		}
	}
}

func TestDecideActionReadAsk(t *testing.T) {
	grant := DefaultConnectorGrant("github")
	got := DecideAction(grant, ActionRules{Read: ChangesAsk}, ConnectorRead, "a")
	if got != ConnectorAsk {
		t.Fatalf("read ask = %s, want ask", got)
	}
}

func TestDecideActionChangeAllowOverridesGrantAsk(t *testing.T) {
	grant := DefaultConnectorGrant("github")
	if grant.Changes != ChangesAsk {
		t.Fatalf("default changes = %s, want ask", grant.Changes)
	}
	got := DecideAction(grant, ActionRules{Change: ChangesAllow}, ConnectorChange, "a")
	if got != ConnectorAllow {
		t.Fatalf("change allow = %s, want allow", got)
	}
}

func TestDecideActionOutOfScopeBlocks(t *testing.T) {
	grant, err := ParseConnectorGrant("github", "these", []string{"a"}, "ask")
	if err != nil {
		t.Fatal(err)
	}
	got := DecideAction(grant, ActionRules{Change: ChangesAllow}, ConnectorChange, "c")
	if got != ConnectorBlock {
		t.Fatalf("out of scope = %s, want block", got)
	}
}
