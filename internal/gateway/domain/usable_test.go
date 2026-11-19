package domain

import (
	"slices"
	"testing"
)

// connected is an own account that works and lists two models.
var connected = Provider{
	ID:       "acc-1",
	Provider: "Fake",
	Adapter:  AdapterFake,
	Status:   StatusConnected,
	Models:   []string{"m1", "m2"},
	Own:      true,
}

// houseAcct is the workspace's account, Connected, with one model.
var houseAcct = Provider{
	ID:       "acc-2",
	Provider: "House",
	Adapter:  AdapterOpenAI,
	Status:   StatusConnected,
	Models:   []string{"h1"},
}

func TestUsableModelNeedsAConnectedAccountThatListsTheModel(t *testing.T) {
	for status, want := range map[string]bool{
		StatusConnected:    true,
		StatusNotConnected: false,
		StatusKeyRejected:  false,
		StatusOffline:      false,
		StatusRateLimited:  false,
	} {
		p := connected
		p.Status = status
		if got := UsableModel(p, "m1"); got != want {
			t.Fatalf("%q: usable m1 = %v", status, got)
		}
	}
	if UsableModel(connected, "m3") {
		t.Fatal("a model the account does not list is usable")
	}
}

func TestUsableModelsListsOnlyTheUsableOnes(t *testing.T) {
	notConnected := connected
	notConnected.Status = StatusNotConnected
	if got := UsableModels(notConnected); len(got) != 0 {
		t.Fatalf("a Not connected account listed %v", got)
	}
	got := UsableModels(connected)
	if !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("usable models = %v", got)
	}
	// A nil answer would read as null on the wire.
	if got := UsableModels(Provider{Status: StatusConnected}); got == nil || len(got) != 0 {
		t.Fatalf("an account with no models listed %v", got)
	}
}

func TestUsableRouteNeedsOneHopOnAnAccountThePersonCanUse(t *testing.T) {
	hops := []UsableHop{
		{AccountID: "theirs", Model: "t1"},
		{AccountID: connected.ID, Model: "m1"},
	}
	if !UsableRoute(UsableRouteView{Name: "chat", Capability: CapChat, Hops: hops}, []Provider{connected, houseAcct}) {
		t.Fatal("a route whose second hop is usable read as unusable")
	}

	// Every hop on another person's account, or on a model the account does
	// not list, leaves the route unusable.
	for name, broken := range map[string][]UsableHop{
		"other person's account": {{AccountID: "theirs", Model: "t1"}},
		"model not listed":       {{AccountID: connected.ID, Model: "m3"}},
		"unknown account":        {{AccountID: "gone", Model: "m1"}},
		"no hops":                {},
	} {
		if UsableRoute(UsableRouteView{Name: "chat", Capability: CapChat, Hops: broken}, []Provider{connected, houseAcct}) {
			t.Fatalf("%s: route read as usable", name)
		}
	}

	// A Not connected account the person can use is not a usable hop.
	notConnected := connected
	notConnected.Status = StatusNotConnected
	if UsableRoute(UsableRouteView{Name: "chat", Hops: []UsableHop{{AccountID: notConnected.ID, Model: "m1"}}}, []Provider{notConnected}) {
		t.Fatal("a Not connected account made a usable hop")
	}
}

func TestUsableAgentModelMatchesTheProviderOwnAccountsFirst(t *testing.T) {
	if UsableAgentModel("", "m1", []Provider{connected}) || UsableAgentModel("Fake", "", []Provider{connected}) {
		t.Fatal("a half-named model resolved")
	}
	if !UsableAgentModel("fake", "m1", []Provider{connected}) {
		t.Fatal("the provider matched case-insensitively failed")
	}
	// An account with no provider label counts as its adapter.
	labelled := connected
	labelled.Provider = ""
	if !UsableAgentModel("Fake", "m1", []Provider{labelled}) {
		t.Fatal("an account of the adapter's name did not resolve")
	}

	// The person's own account comes before a workspace one: a broken own
	// account is not covered by a working workspace one, and the other way
	// round a workspace one serves when no own account works.
	brokenOwn := connected
	brokenOwn.Status = StatusKeyRejected
	if UsableAgentModel("Fake", "m1", []Provider{brokenOwn, houseAcct}) {
		t.Fatal("a rejected own account resolved to the workspace's other provider")
	}
	if !UsableAgentModel("House", "h1", []Provider{brokenOwn, houseAcct}) {
		t.Fatal("a workspace model did not resolve")
	}
}

func TestUsableAgentResolvesTheModelTheRouteOrTheDefault(t *testing.T) {
	routes := []UsableRouteView{
		{Name: "fast", Capability: CapChat, Hops: []UsableHop{{AccountID: connected.ID, Model: "m1"}}},
		{Name: "dead", Capability: CapChat, Hops: []UsableHop{{AccountID: "theirs", Model: "t1"}}},
		{Name: "embed-all", Capability: CapEmbed, Hops: []UsableHop{{AccountID: connected.ID, Model: "m1"}}},
	}
	accounts := []Provider{connected, houseAcct}

	// Model only.
	agent := AgentView{Enabled: true, ModelProvider: "Fake", ModelID: "m2"}
	if !UsableAgent(agent, routes, accounts, "chat") {
		t.Fatal("an agent whose model resolves was not usable")
	}
	// The model alone carries it even when the default route is unknown.
	if !UsableAgent(agent, routes, accounts, "") {
		t.Fatal("a usable model needed no route")
	}

	// Route only.
	byRoute := AgentView{Enabled: true, Route: "fast"}
	if !UsableAgent(byRoute, routes, accounts, "chat") {
		t.Fatal("an agent whose route is usable was not usable")
	}

	// No route and no model: the install default route decides.
	byDefault := AgentView{Enabled: true}
	if !UsableAgent(byDefault, routes, accounts, "fast") {
		t.Fatal("an agent on a usable default route was not usable")
	}
	if UsableAgent(byDefault, routes, accounts, "dead") {
		t.Fatal("an agent on a default route with no usable hop was usable")
	}
	if UsableAgent(byDefault, routes, accounts, "") {
		t.Fatal("an agent with no default route known was usable")
	}
	// An embed route cannot carry a chat, so it is no default for an agent.
	if UsableAgent(byDefault, routes, accounts, "embed-all") {
		t.Fatal("an embed default route made the agent usable")
	}

	// Disabled, whatever its model or route.
	off := AgentView{Enabled: false, ModelProvider: "Fake", ModelID: "m1", Route: "fast"}
	if UsableAgent(off, routes, accounts, "fast") {
		t.Fatal("a disabled agent was usable")
	}

	// A model and a route stand together: either resolving carries the
	// agent, but the default route never steps in for a named one.
	mixed := AgentView{Enabled: true, ModelProvider: "Fake", ModelID: "m3", Route: "fast"}
	if !UsableAgent(mixed, routes, accounts, "chat") {
		t.Fatal("a broken model with a usable route was not usable")
	}
	mixed.Route = "dead"
	if UsableAgent(mixed, routes, accounts, "fast") {
		t.Fatal("a broken model and a broken route made the agent usable")
	}
	// A named model alone answers for the agent, even when the default
	// route would work.
	brokenModel := AgentView{Enabled: true, ModelProvider: "Fake", ModelID: "m3"}
	if UsableAgent(brokenModel, routes, accounts, "fast") {
		t.Fatal("a broken model was covered by the default route")
	}
}

func TestUsableListsOnlyChatModelsAndCountsUncataloguedAsChat(t *testing.T) {
	mixed := Provider{
		ID:       "acc-mix",
		Provider: "OpenAI",
		Adapter:  AdapterOpenAI,
		Status:   StatusConnected,
		Models:   []string{"gpt-4o", "text-embedding-3-small", "dall-e-3", "rerank-1", "custom-uncatalogued"},
		ModelKinds: map[string]ModelKind{
			"gpt-4o":                 KindChat,
			"text-embedding-3-small": KindEmbedding,
			"dall-e-3":               KindImage,
			"rerank-1":               KindRerank,
		},
	}

	got := UsableModels(mixed)
	want := []string{"gpt-4o", "custom-uncatalogued"}
	if !slices.Equal(got, want) {
		t.Fatalf("UsableModels = %v, want %v", got, want)
	}
	if UsableModel(mixed, "text-embedding-3-small") || UsableModel(mixed, "dall-e-3") || UsableModel(mixed, "rerank-1") {
		t.Fatal("non-chat model was marked usable for chat")
	}
	if !UsableModel(mixed, "custom-uncatalogued") {
		t.Fatal("uncatalogued model should count as chat")
	}
	if UsableAgentModel("OpenAI", "text-embedding-3-small", []Provider{mixed}) {
		t.Fatal("embedding model was usable for an agent")
	}
	if UsableRoute(UsableRouteView{Name: "chat", Capability: CapChat, Hops: []UsableHop{{AccountID: mixed.ID, Model: "text-embedding-3-small"}}}, []Provider{mixed}) {
		t.Fatal("chat route accepted an embedding model")
	}
	if !UsableRoute(UsableRouteView{Name: "embed", Capability: CapEmbed, Hops: []UsableHop{{AccountID: mixed.ID, Model: "text-embedding-3-small"}}}, []Provider{mixed}) {
		t.Fatal("embed route rejected an embedding model")
	}
}
