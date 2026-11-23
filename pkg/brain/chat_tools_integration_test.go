//go:build integration

package brain

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// TestBrainChatActionToolsRespectAuthorNarrowedRLS verifies that the production
// chatActionTools wiring in pkg/brain narrows teams and clears cross-object
// shares when re-fetching live access from identity.MemberService.
func TestBrainChatActionToolsRespectAuthorNarrowedRLS(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, memory.Schema, memory.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	members := identity.Members(pool, nil, "", nil, nil)
	taskStore := tasks.NewStore(pool, nil)
	agentStore := agents.NewStore(pool)
	memoryFacts := memory.NewFacts(pool)

	person := func(name, role string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := owner.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			SELECT lower($1::text) || '@example.com', sha256(convert_to(lower($1::text) || '@example.com', 'UTF8')), 'k1', $1::text, role.id, now()
			FROM identity.roles AS role
			WHERE role.key = $2::text
			RETURNING id`, name, role).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ada := person("Ada", "owner")
	bob := person("Bob", "owner")

	mkTeam := func(name string) string {
		t.Helper()
		var id string
		if err := owner.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, u := range []uuid.UUID{ada, bob} {
			if _, err := owner.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2)`, id, u); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	teamA := mkTeam("Alpha")
	teamB := mkTeam("Beta")

	bobTaskAccess, ok, err := taskAccessFor(ctx, members, identity.UserID{UUID: bob})
	if err != nil || !ok {
		t.Fatalf("bob task access = %v %v", ok, err)
	}
	taskA, err := tasks.OpenTask(ctx, taskStore, nil, bobTaskAccess, tasks.Opening{Goal: "Alpha task"})
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := tasks.OpenTask(ctx, taskStore, nil, bobTaskAccess, tasks.Opening{Goal: "Beta task"})
	if err != nil {
		t.Fatal(err)
	}
	taskShared, err := tasks.OpenTask(ctx, taskStore, nil, bobTaskAccess, tasks.Opening{Goal: "Direct shared task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, taskA.ID, teamA); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, taskB.ID, teamB); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', $1::uuid, $2, 'viewer', $3)`, taskShared.ID, ada, bob); err != nil {
		t.Fatal(err)
	}

	bobAgentAccess, ok, err := agentAccess(ctx, members, identity.UserID{UUID: bob})
	if err != nil || !ok {
		t.Fatalf("bob agent access = %v %v", ok, err)
	}
	agentA, err := agents.CreateAgent(ctx, agentStore, nil, bobAgentAccess, agents.AgentFields{
		Name: "Alpha Agent", Enabled: true, Visibility: agents.VisibilityTeam, TeamIDs: []string{teamA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agents.CreateAgent(ctx, agentStore, nil, bobAgentAccess, agents.AgentFields{
		Name: "Beta Agent", Enabled: true, Visibility: agents.VisibilityTeam, TeamIDs: []string{teamB},
	}); err != nil {
		t.Fatal(err)
	}

	actionTools := chatActionTools{tasks: taskStore, agents: agentStore, memory: memoryFacts, members: members}
	adaChat := chat.Access{UserID: ada.String()}
	teamAScope := chat.AccessScope{
		UserID:           ada.String(),
		ObjectVisibility: agentloop.VisibilityTeam,
		VisibilityTeamID: teamA,
	}

	gotTasks, err := actionTools.ListTasks(ctx, adaChat, teamAScope, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotTasks) != 1 || gotTasks[0].ID != taskA.ID {
		t.Fatalf("brain ListTasks in Team A = %+v, want only %s (must exclude %s and %s)", gotTasks, taskA.ID, taskB.ID, taskShared.ID)
	}

	gotAgents, err := actionTools.ListAgents(ctx, adaChat, teamAScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotAgents) != 1 || gotAgents[0].ID != agentA.ID {
		t.Fatalf("brain ListAgents in Team A = %+v, want only %s (must exclude Beta Agent even for owner with settings:manage)", gotAgents, agentA.ID)
	}

	// OpenTask, SaveMemory, and CreateAutomation round-trip through brain's adapter.
	created, err := actionTools.OpenTask(ctx, adaChat, teamAScope, chat.NewTaskInput{Goal: "Created from chat"})
	if err != nil || created.ID == "" || created.Status != "Preparing" {
		t.Fatalf("brain OpenTask = %+v err=%v", created, err)
	}
	factID, err := actionTools.SaveMemory(ctx, ada.String(), "Prefers UTC")
	if err != nil || factID == "" {
		t.Fatalf("brain SaveMemory = %q err=%v", factID, err)
	}
	tplID, autoID, err := actionTools.CreateAutomation(ctx, adaChat, teamAScope, chat.AutomationInput{
		Goal: "Daily check", AgentID: agentA.ID, Kind: "cron", Spec: "0 8 * * *", BudgetCents: 250,
	})
	if err != nil || tplID == "" || autoID == "" {
		t.Fatalf("brain CreateAutomation = tpl=%q auto=%q err=%v", tplID, autoID, err)
	}
}
