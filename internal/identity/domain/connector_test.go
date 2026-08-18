package domain

import (
	"errors"
	"testing"
)

func TestParseConnectorGrant(t *testing.T) {
	got, err := ParseConnectorGrant("github", "all", nil, "")
	if err != nil || got.Scope != ScopeAll || got.Changes != ChangesAsk {
		t.Fatalf("default = %+v %v", got, err)
	}
	got, err = ParseConnectorGrant("github", "these", []string{"upmo-ai/upmo"}, "allow")
	if err != nil || got.Scope != ScopeThese || got.Changes != ChangesAllow || len(got.Resources) != 1 {
		t.Fatalf("these = %+v %v", got, err)
	}
	if _, err := ParseConnectorGrant("GitHub", "all", nil, "ask"); !errors.Is(err, ErrConnector) {
		t.Fatalf("slug = %v", err)
	}
	if _, err := ParseConnectorGrant("github", "all", []string{"x"}, "ask"); !errors.Is(err, ErrResourceScope) {
		t.Fatalf("all with ids = %v", err)
	}
	if _, err := ParseConnectorGrant("github", "these", nil, "ask"); !errors.Is(err, ErrResourceScope) {
		t.Fatalf("these empty = %v", err)
	}
	if _, err := ParseConnectorGrant("github", "all", nil, "maybe"); !errors.Is(err, ErrChanges) {
		t.Fatalf("changes = %v", err)
	}
}

func TestConnectorDecide(t *testing.T) {
	allAsk := DefaultConnectorGrant("github")
	theseAllow, err := ParseConnectorGrant("github", "these", []string{"a", "b"}, "allow")
	if err != nil {
		t.Fatal(err)
	}
	allBlock, err := ParseConnectorGrant("github", "all", nil, "block")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		grant    ConnectorGrant
		action   ConnectorAction
		resource string
		want     ConnectorVerdict
	}{
		{allAsk, ConnectorRead, "", ConnectorAllow},
		{allAsk, ConnectorChange, "a", ConnectorAsk},
		{allAsk, ConnectorDelete, "a", ConnectorAsk},
		{theseAllow, ConnectorRead, "a", ConnectorAllow},
		{theseAllow, ConnectorChange, "a", ConnectorAllow},
		{theseAllow, ConnectorDelete, "a", ConnectorAsk},
		{theseAllow, ConnectorRead, "c", ConnectorBlock},
		{theseAllow, ConnectorChange, "c", ConnectorBlock},
		{theseAllow, ConnectorDelete, "c", ConnectorBlock},
		{allBlock, ConnectorRead, "a", ConnectorAllow},
		{allBlock, ConnectorChange, "a", ConnectorBlock},
		{allBlock, ConnectorDelete, "a", ConnectorAsk},
	}
	for _, tc := range cases {
		got := ConnectorDecide(tc.grant, tc.action, tc.resource)
		if got != tc.want {
			t.Fatalf("%s %s %q = %s, want %s", tc.grant.Changes, tc.action, tc.resource, got, tc.want)
		}
	}
}
