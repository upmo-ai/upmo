package app

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func TestMarkUsableReadsTheHopsAndTheAccounts(t *testing.T) {
	routes := []ListedRoute{
		{
			Name:       "chat",
			Capability: "chat",
			Hops: []ListedHop{
				{Position: 1, Adapter: "fake", Model: "m1", AccountID: "theirs"},
				{Position: 2, Adapter: "fake", Model: "m2", AccountID: "mine"},
			},
		},
		{
			Name:       "dead",
			Capability: "chat",
			Hops:       []ListedHop{{Position: 1, Adapter: "fake", Model: "m1", AccountID: "theirs"}},
		},
	}
	accounts := []domain.Provider{
		{ID: "mine", Status: domain.StatusConnected, Models: []string{"m2"}, Own: true},
	}
	MarkUsable(routes, accounts)
	if !routes[0].Usable || routes[1].Usable {
		t.Fatalf("usable flags = %v, %v", routes[0].Usable, routes[1].Usable)
	}

	// An account that cannot be used leaves every route unusable.
	notConnected := []domain.Provider{{ID: "mine", Status: domain.StatusNotConnected, Models: []string{"m2"}, Own: true}}
	MarkUsable(routes, notConnected)
	if routes[0].Usable || routes[1].Usable {
		t.Fatal("a Not connected account made a route usable")
	}
}

func TestUsableAgentCarriesTheAgentAndTheRoutesOver(t *testing.T) {
	routes := []ListedRoute{{
		Name:       "chat",
		Capability: "chat",
		Hops:       []ListedHop{{Position: 1, Adapter: "fake", Model: "m1", AccountID: "mine"}},
	}}
	accounts := []domain.Provider{{ID: "mine", Provider: "Fake", Status: domain.StatusConnected, Models: []string{"m1"}, Own: true}}

	if !UsableAgent(true, "", "", "", routes, accounts, "chat") {
		t.Fatal("an agent on the usable default route was not usable")
	}
	if UsableAgent(true, "", "", "", routes, accounts, "other") {
		t.Fatal("an agent on an unknown default route was usable")
	}
	if UsableAgent(false, "", "fake", "m1", routes, accounts, "chat") {
		t.Fatal("a disabled agent was usable")
	}
	if !UsableAgent(true, "", "Fake", "m1", routes, accounts, "other") {
		t.Fatal("an agent with a usable model was not usable")
	}
}
