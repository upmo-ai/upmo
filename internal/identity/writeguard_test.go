package identity_test

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
)

func TestTightenWrite(t *testing.T) {
	armed := identity.WriteExposure{Untrusted: true, Private: true, External: true}
	allow := identity.ConnectorDecide(identity.ConnectorGrant{
		Connector: "github", Scope: identity.ScopeAll, Changes: identity.ChangesAllow,
	}, identity.ConnectorChange, "repo")
	if allow != "allow" {
		t.Fatalf("grant allow = %s", allow)
	}
	if got := identity.TightenWrite(allow, armed); got != "ask" {
		t.Fatalf("allow armed = %s", got)
	}
	if got := identity.TightenWrite("block", armed); got != "block" {
		t.Fatalf("block armed = %s", got)
	}
	verdict := identity.ConnectorDecide(identity.DefaultConnectorGrant("github"), identity.ConnectorChange, "repo")
	if verdict != "ask" {
		t.Fatalf("default change = %s", verdict)
	}
	if got := identity.TightenWrite(verdict, armed); got != "ask" {
		t.Fatalf("ask armed = %s", got)
	}
}
