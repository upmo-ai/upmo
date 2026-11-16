//go:build integration

package audit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat"
	chatapp "github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const agentOwnerID = "018f3b2c-7a10-7c4a-8e11-555555555555"

// agentAccess holds what a person who may create and share agents holds.
var agentAccess = chatapp.Access{
	UserID: agentOwnerID,
	Allow:  "agent:create,agent:view,agent:edit,agent:delete,settings:manage",
}

func boolPtr(v bool) *bool { return &v }

// madeAgent stores one private agent and returns it.
func madeAgent(t *testing.T, store chatapp.AgentBook) chatapp.Agent {
	t.Helper()
	made, err := chatapp.CreateAgent(context.Background(), store, nil, agentAccess, chatapp.AgentFields{
		Name:          "Scout",
		Description:   "Finds things",
		PromptOverlay: "Be brief.",
		Route:         "work",
		ModelProvider: "fake",
		ModelID:       "model-x",
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return made
}

func TestAgentCreateSealsItsAction(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)

	made := madeAgent(t, store)

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.AgentCreate {
		t.Fatalf("events = %v, want one agent.create", actions(rows))
	}
	row := rows[0]
	// The store recorded the event, so the middleware left no fallback.
	if row.Reason != "" {
		t.Fatalf("reason = %q, want the store's own event", row.Reason)
	}
	if row.TargetType != "agent" || row.TargetID != made.ID {
		t.Fatalf("target = %s %s, want the stored agent", row.TargetType, row.TargetID)
	}
	if got := changeOf(t, row, "enabled"); got.To != true {
		t.Fatalf("enabled = %v, want the stored agent", got.To)
	}
	if got := changeOf(t, row, "route"); got.To != "work" {
		t.Fatalf("route = %v, want the stored route", got.To)
	}
	if got := changeOf(t, row, "model_provider"); got.To != "fake" {
		t.Fatalf("model_provider = %v, want the stored provider", got.To)
	}
	if got := changeOf(t, row, "model_id"); got.To != "model-x" {
		t.Fatalf("model_id = %v, want the stored model", got.To)
	}
	if got := changeOf(t, row, "visibility"); got.To != domain.VisibilityPrivate {
		t.Fatalf("visibility = %v, want the private agent", got.To)
	}
	if got := changeOf(t, row, "team_ids"); !sameIDs(got.To) {
		t.Fatalf("team_ids = %v, want a private agent's empty teams", got.To)
	}
	mustNotContain(t, row, "Be brief.", "Scout", "Finds things")
}

func TestAgentUpdateSealsItsChangedFields(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)
	ctx := context.Background()

	made := madeAgent(t, store)
	sealed()

	changed, err := chatapp.ChangeAgent(ctx, store, nil, agentAccess, made.ID, chatapp.AgentChange{
		PromptOverlay: strPtr("Be terse."),
		Route:         strPtr("research"),
		Enabled:       boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.AgentUpdate {
		t.Fatalf("events = %v, want one agent.update", actions(rows))
	}
	row := rows[0]
	if row.TargetID != changed.ID || row.Reason != "" {
		t.Fatalf("event = %s %q, want the store's own update", row.TargetID, row.Reason)
	}
	if strings.Join(row.Changes.Fields, ",") != "enabled,prompt_overlay,route" {
		t.Fatalf("fields = %v, want the changed field names", row.Changes.Fields)
	}
	if got := changeOf(t, row, "enabled"); got.From != true || got.To != false {
		t.Fatalf("enabled = %v to %v, want the change", got.From, got.To)
	}
	if got := changeOf(t, row, "route"); got.From != "work" || got.To != "research" {
		t.Fatalf("route = %v to %v, want the change", got.From, got.To)
	}
	if len(row.Changes.Values) != 2 {
		t.Fatalf("values = %s, want the route and the flag alone", changesJSON(t, row))
	}
	mustNotContain(t, row, "Be brief.", "Be terse.", "Scout")
}

func TestAgentShareSealsTheMove(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	team := newTeam(t, owner, "Ops")
	store := chat.NewStore(appPool, nil)
	ctx := context.Background()

	made := madeAgent(t, store)
	sealed()

	teams := []string{team}
	if _, err := chatapp.ChangeAgent(ctx, store, nil, agentAccess, made.ID, chatapp.AgentChange{
		Visibility: strPtr(domain.VisibilityTeam),
		TeamIDs:    &teams,
	}); err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 2 || rows[0].Action != audit.AgentUpdate || rows[1].Action != audit.AgentShare {
		t.Fatalf("events = %v, want the update and the share", actions(rows))
	}
	update, share := rows[0], rows[1]
	if strings.Join(update.Changes.Fields, ",") != "team_ids,visibility" {
		t.Fatalf("update fields = %v, want the sharing move", update.Changes.Fields)
	}
	if got := changeOf(t, update, "visibility"); got.From != domain.VisibilityPrivate || got.To != domain.VisibilityTeam {
		t.Fatalf("visibility = %v to %v, want the move", got.From, got.To)
	}
	if got := changeOf(t, update, "team_ids"); !sameIDs(got.From) || !sameIDs(got.To, team) {
		t.Fatalf("team_ids = %v to %v, want the move", got.From, got.To)
	}
	if strings.Join(share.Changes.Fields, ",") != "team_ids,visibility" {
		t.Fatalf("share fields = %v, want the move", share.Changes.Fields)
	}
	if got := changeOf(t, share, "visibility"); got.From != domain.VisibilityPrivate || got.To != domain.VisibilityTeam {
		t.Fatalf("share visibility = %v to %v, want the move", got.From, got.To)
	}
	mustNotContain(t, update, "Scout")
	mustNotContain(t, share, "Scout")
}

func TestAgentDeleteSealsItsAction(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)

	made := madeAgent(t, store)
	sealed()
	if err := chatapp.DeleteAgent(context.Background(), store, agentAccess, made.ID); err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.AgentDelete {
		t.Fatalf("events = %v, want one agent.delete", actions(rows))
	}
	row := rows[0]
	if row.TargetType != "agent" || row.TargetID != made.ID || row.Reason != "" {
		t.Fatalf("event = %s %s %q, want the store's own delete", row.TargetType, row.TargetID, row.Reason)
	}
	mustNotContain(t, row, "Scout")
}

func TestAgentUpdateRepeatRecordsNothing(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)

	made := madeAgent(t, store)
	sealed()

	// The change repeats the stored agent, so no row changed.
	if _, err := chatapp.ChangeAgent(context.Background(), store, nil, agentAccess, made.ID,
		chatapp.AgentChange{Route: strPtr("work")}); err != nil {
		t.Fatal(err)
	}
	if rows := sealed(); len(rows) != 0 {
		t.Fatalf("a repeated change sealed %v", actions(rows))
	}
}

func TestAgentRouteSealsWithThePersonActor(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)

	mux := http.NewServeMux()
	chat.AgentsHandler{
		Agents: store,
		Access: func(r *http.Request) (chatapp.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: agentOwnerID, CredentialType: "session"})
			return agentAccess, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agents",
		strings.NewReader(`{"name":"Routed Agent","route":"work"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.AgentCreate {
		t.Fatalf("events = %v, want one agent.create", actions(rows))
	}
	row := rows[0]
	// The adapter recorded the event, so the middleware left no fallback.
	if row.Reason != "" {
		t.Fatalf("reason = %q, want the store's own event", row.Reason)
	}
	if row.ActorType != "user" || row.ActorID != agentOwnerID || row.CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.CredentialType)
	}
	mustNotContain(t, row, "Routed Agent")
}

func TestAgentUpdateRefusedForMemberRecordsDenied(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	team := newTeam(t, owner, "Ops")
	store := chat.NewStore(appPool, nil)
	ctx := context.Background()

	teams := []string{team}
	made, err := chatapp.ChangeAgent(ctx, store, nil, agentAccess, madeAgent(t, store).ID, chatapp.AgentChange{
		Visibility: strPtr(domain.VisibilityTeam),
		TeamIDs:    &teams,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed()

	// A member of the team may use the agent, but a shared one is managed by
	// the people who hold settings:manage.
	stranger := chatapp.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-666666666666",
		Allow:  "agent:view",
		Teams:  "{" + team + "}",
	}
	mux := http.NewServeMux()
	chat.AgentsHandler{
		Agents: store,
		Access: func(r *http.Request) (chatapp.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: stranger.UserID, CredentialType: "session"})
			return stranger, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/agents/"+made.ID,
		strings.NewReader(`{"name":"Renamed"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member change = %d %s, want 403", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.AgentUpdate {
		t.Fatalf("events = %v, want one denied agent.update", actions(rows))
	}
	if rows[0].Outcome != audit.Denied || rows[0].ActorID != stranger.UserID {
		t.Fatalf("event = %s %s %s, want the refused member denied", rows[0].Action, rows[0].Outcome, rows[0].ActorID)
	}
	mustNotContain(t, rows[0], "Renamed")
}

func TestAgentsContextSealsCreateUpdateShareDelete(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)
	sealed := newSealer(t, owner)
	store := agents.NewStore(appPool)
	team := newTeam(t, owner, "Ops")

	access := agents.Access{
		UserID: agentOwnerID,
		Allow:  "agent:create,agent:view,agent:edit,agent:delete,settings:manage",
	}
	made, err := agents.CreateAgent(ctx, store, nil, access, agents.AgentFields{
		Name:                 "Bookkeeper",
		Description:          "AP agent",
		Instructions:         "Secret instructions",
		DefaultRoute:         "work",
		DefaultModelProvider: "fake",
		DefaultModelID:       "model-x",
		Enabled:              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.AgentCreate || rows[0].TargetID != made.ID {
		t.Fatalf("create events = %+v", rows)
	}
	mustNotContain(t, rows[0], "Secret instructions", "Bookkeeper")

	teams := []string{team}
	if _, err := agents.ChangeAgent(ctx, store, nil, access, made.ID, agents.AgentChange{
		Instructions: strPtr("Updated secret"),
		Visibility:   strPtr(agents.VisibilityTeam),
		TeamIDs:      &teams,
	}); err != nil {
		t.Fatal(err)
	}
	rows = sealed()
	if len(rows) != 2 || rows[0].Action != audit.AgentUpdate || rows[1].Action != audit.AgentShare {
		t.Fatalf("update/share events = %v", actions(rows))
	}
	mustNotContain(t, rows[0], "Updated secret", "Bookkeeper")

	mux := http.NewServeMux()
	agents.Handler{
		Agents: store,
		Access: func(r *http.Request) (agents.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: agentOwnerID, CredentialType: "session"})
			return access, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/agents/"+made.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.AgentDelete || rows[0].TargetID != made.ID {
		t.Fatalf("delete events = %+v", rows)
	}
}
