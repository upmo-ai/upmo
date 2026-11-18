//go:build integration

package audit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const agentOwnerID = "018f3b2c-7a10-7c4a-8e11-555555555555"

// agentsRig migrates the agents context and returns the owner and app pools.
func agentsRig(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
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
	return owner, pgtest.App(t, owner)
}

func TestAgentsContextSealsCreateUpdateShareDelete(t *testing.T) {
	owner, appPool := agentsRig(t)
	ctx := context.Background()
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

func TestAgentUpdateRepeatRecordsNothing(t *testing.T) {
	owner, appPool := agentsRig(t)
	ctx := context.Background()
	sealed := newSealer(t, owner)
	store := agents.NewStore(appPool)
	access := agents.Access{UserID: agentOwnerID, Allow: "agent:create,agent:view,agent:edit"}

	made, err := agents.CreateAgent(ctx, store, nil, access, agents.AgentFields{
		Name:         "Bookkeeper",
		DefaultRoute: "work",
		Enabled:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows := sealed(); len(rows) != 1 || rows[0].Action != audit.AgentCreate || rows[0].TargetID != made.ID {
		t.Fatalf("create events = %v", actions(rows))
	}

	// The change repeats the stored agent, so no row changed.
	if _, err := agents.ChangeAgent(ctx, store, nil, access, made.ID,
		agents.AgentChange{DefaultRoute: strPtr("work")}); err != nil {
		t.Fatal(err)
	}
	if rows := sealed(); len(rows) != 0 {
		t.Fatalf("a repeated change sealed %v", actions(rows))
	}
}

func TestAgentRouteSealsWithThePersonActor(t *testing.T) {
	owner, appPool := agentsRig(t)
	sealed := newSealer(t, owner)
	store := agents.NewStore(appPool)
	access := agents.Access{UserID: agentOwnerID, Allow: "agent:create,agent:view"}

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
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agents",
		strings.NewReader(`{"name":"Routed Agent","default_route":"work"}`)))
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
	owner, appPool := agentsRig(t)
	ctx := context.Background()
	sealed := newSealer(t, owner)
	team := newTeam(t, owner, "Ops")
	store := agents.NewStore(appPool)
	access := agents.Access{
		UserID: agentOwnerID,
		Allow:  "agent:create,agent:view,agent:edit,settings:manage",
	}

	created, err := agents.CreateAgent(ctx, store, nil, access, agents.AgentFields{
		Name:         "Bookkeeper",
		DefaultRoute: "work",
		Enabled:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	teams := []string{team}
	made, err := agents.ChangeAgent(ctx, store, nil, access, created.ID, agents.AgentChange{
		Visibility: strPtr(agents.VisibilityTeam),
		TeamIDs:    &teams,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed()

	// A member of the team may use the agent, but a shared one is managed by
	// the people who hold settings:manage.
	member := agents.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-666666666666",
		Allow:  "agent:view",
		Teams:  "{" + team + "}",
	}
	mux := http.NewServeMux()
	agents.Handler{
		Agents: store,
		Access: func(r *http.Request) (agents.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: member.UserID, CredentialType: "session"})
			return member, true
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
	if rows[0].Outcome != audit.Denied || rows[0].ActorID != member.UserID {
		t.Fatalf("event = %s %s %s, want the refused member denied", rows[0].Action, rows[0].Outcome, rows[0].ActorID)
	}
	mustNotContain(t, rows[0], "Renamed")
}
