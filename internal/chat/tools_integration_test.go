//go:build integration

package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/tasks"
)

const (
	toolsTeamA = "018f3b2c-7a10-7c4a-8e11-dddddddddd01"
	toolsTeamB = "018f3b2c-7a10-7c4a-8e11-dddddddddd02"
)

// rigActionTools wires the real Postgres-backed tasks, agents, and memory
// stores into chat.ActionTools. It relies on chat's narrowed Access passed by
// the direct tool handlers so any regression in narrowing leaks directly into
// real Postgres RLS.
type rigActionTools struct {
	tasks  tasks.Store
	agents agents.Store
	memory memory.Facts
}

func (t rigActionTools) ListTasks(ctx context.Context, access chat.Access, _ chat.AccessScope, limit int) ([]chat.TaskItem, error) {
	rows, err := tasks.ListTasks(ctx, t.tasks, tasks.Access{
		UserID:     access.UserID,
		Allow:      access.Allow,
		Teams:      access.Teams,
		SharedView: access.SharedView,
		SharedEdit: access.SharedEdit,
	})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]chat.TaskItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, chat.TaskItem{
			ID:        row.ID,
			Title:     row.Title,
			Status:    string(row.Status),
			Phase:     string(row.Phase),
			UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

func (t rigActionTools) OpenTask(ctx context.Context, access chat.Access, _ chat.AccessScope, in chat.NewTaskInput) (chat.TaskItem, error) {
	created, err := tasks.OpenTask(ctx, t.tasks, nil, tasks.Access{
		UserID:     access.UserID,
		Allow:      access.Allow,
		Teams:      access.Teams,
		SharedView: access.SharedView,
		SharedEdit: access.SharedEdit,
	}, tasks.Opening{Goal: in.Goal})
	if err != nil {
		return chat.TaskItem{}, err
	}
	return chat.TaskItem{
		ID:     created.ID,
		Title:  created.Goal,
		Status: string(created.Status),
		Phase:  string(created.Status),
	}, nil
}

func (t rigActionTools) SaveMemory(ctx context.Context, userID, claim string) (string, error) {
	return memory.AddFact(ctx, t.memory, userID, claim, time.Now().UTC())
}

func (t rigActionTools) ListAgents(ctx context.Context, access chat.Access, _ chat.AccessScope) ([]chat.AgentItem, error) {
	rows, err := agents.ListAgents(ctx, t.agents, agents.Access{
		UserID: access.UserID,
		Allow:  "agent:view",
		Teams:  access.Teams,
	})
	if err != nil {
		return nil, err
	}
	out := make([]chat.AgentItem, 0, len(rows))
	for _, a := range rows {
		if !a.Enabled || !a.Share.CanUse {
			continue
		}
		out = append(out, chat.AgentItem{
			ID:          a.ID,
			Name:        a.Name,
			Description: a.Description,
		})
	}
	return out, nil
}

func (t rigActionTools) CreateAutomation(ctx context.Context, access chat.Access, _ chat.AccessScope, in chat.AutomationInput) (string, string, error) {
	taskAccess := tasks.Access{
		UserID:     access.UserID,
		Allow:      access.Allow,
		Teams:      access.Teams,
		SharedView: access.SharedView,
		SharedEdit: access.SharedEdit,
	}
	templateID, err := tasks.SaveTemplate(ctx, t.tasks, taskAccess, "", tasks.Template{
		Goal:    in.Goal,
		AgentID: in.AgentID,
		Budget:  in.BudgetCents,
	})
	if err != nil {
		return "", "", err
	}
	automationID, err := tasks.SaveAutomation(ctx, t.tasks, taskAccess, templateID, in.Kind, in.Spec)
	if err != nil {
		return "", "", err
	}
	return templateID, automationID, nil
}

func newToolsRig(t *testing.T) (*rig, rigActionTools) {
	t.Helper()
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	if err := migrate.Up(ctx, r.owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	tools := rigActionTools{
		tasks:  tasks.NewStore(r.pool, r.work),
		agents: r.agents,
		memory: memory.NewFacts(r.pool),
	}
	r.deps.Tools = tools
	worker, err := app.NewReplyWorker(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	r.worker = worker
	return r, tools
}

func seedToolsIdentity(t *testing.T, r *rig) {
	t.Helper()
	ctx := context.Background()
	for _, u := range []struct{ id, email, role string }{
		{ownerUser, "tools-owner@example.com", "owner"},
		{otherUser, "tools-other@example.com", "member"},
	} {
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3), now())
			ON CONFLICT (id) DO NOTHING`, u.id, u.email, u.role); err != nil {
			t.Fatal(err)
		}
	}
	for _, team := range []struct{ id, name string }{
		{toolsTeamA, "Team A"},
		{toolsTeamB, "Team B"},
	} {
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO identity.teams (id, name) VALUES ($1::uuid, $2) ON CONFLICT (id) DO NOTHING`,
			team.id, team.name); err != nil {
			t.Fatal(err)
		}
		for _, userID := range []string{ownerUser, otherUser} {
			if _, err := r.owner.Exec(ctx, `
				INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`,
				team.id, userID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestInProcessUpmoToolsRespectAuthorNarrowedRLS(t *testing.T) {
	r, tools := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()

	// Seed Team A task, Team B task, and a private cross-shared task (all owned by otherUser
	// so visibility/share rules govern whether ownerUser sees them).
	otherTaskAccess := tasks.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit", Teams: "{" + toolsTeamA + "," + toolsTeamB + "}"}
	taskA, err := tasks.OpenTask(ctx, tools.tasks, nil, otherTaskAccess, tasks.Opening{Goal: "Team A task"})
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := tasks.OpenTask(ctx, tools.tasks, nil, otherTaskAccess, tasks.Opening{Goal: "Team B task"})
	if err != nil {
		t.Fatal(err)
	}
	taskShared, err := tasks.OpenTask(ctx, tools.tasks, nil, otherTaskAccess, tasks.Opening{Goal: "Cross shared task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, taskA.ID, toolsTeamA); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, taskB.ID, toolsTeamB); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', $1::uuid, $2::uuid, 'viewer', $3::uuid)`, taskShared.ID, ownerUser, otherUser); err != nil {
		t.Fatal(err)
	}

	// Seed an enabled Team A agent, a disabled Team A agent, and an enabled Team B agent.
	adminAgentAccess := agents.Access{
		UserID: otherUser,
		Allow:  "agent:create,agent:view,agent:edit,settings:manage",
		Teams:  "{" + toolsTeamA + "," + toolsTeamB + "}",
	}
	agentA, err := agents.CreateAgent(ctx, r.agents, nil, adminAgentAccess, agents.AgentFields{
		Name:        "Team A Agent",
		Description: "Helps Team A",
		Enabled:     true,
		Visibility:  agents.VisibilityTeam,
		TeamIDs:     []string{toolsTeamA},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = agents.CreateAgent(ctx, r.agents, nil, adminAgentAccess, agents.AgentFields{
		Name:        "Disabled Team A Agent",
		Description: "Off",
		Enabled:     false,
		Visibility:  agents.VisibilityTeam,
		TeamIDs:     []string{toolsTeamA},
	})
	if err != nil {
		t.Fatal(err)
	}
	agentB, err := agents.CreateAgent(ctx, r.agents, nil, adminAgentAccess, agents.AgentFields{
		Name:        "Team B Agent",
		Description: "Helps Team B",
		Enabled:     true,
		Visibility:  agents.VisibilityTeam,
		TeamIDs:     []string{toolsTeamB},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Give ownerUser membership in BOTH Team A and Team B plus an explicit share on taskShared.
	ownerFull := chat.Access{
		UserID:     ownerUser,
		Allow:      "chat:create,chat:view,chat:edit,task:create,task:view,task:edit,agent:view,settings:manage",
		Teams:      "{" + toolsTeamA + "," + toolsTeamB + "}",
		SharedView: "{" + taskShared.ID + "}",
		SharedEdit: "{" + taskShared.ID + "}",
	}
	r.setAccess(ownerUser, ownerFull)

	// Open a chat and set it to visibility = 'team', visibility_team_id = Team A.
	chatID := r.open()
	if _, err := r.owner.Exec(ctx, `UPDATE chat.chats SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, chatID, toolsTeamA); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerFull, chatID, "list what I can see", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil,
				stubTool{ID: "t1", Name: "list_tasks", Args: `{"limit":20}`},
				stubTool{ID: "a1", Name: "list_agents", Args: `{}`},
			)
			return
		}
		s.delta("listed")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}

	calls := r.stub.seen()
	if len(calls) != 2 {
		t.Fatalf("gateway calls = %d, want 2", len(calls))
	}
	// save_memory must not be offered in a team-shared chat.
	for _, tool := range calls[0].Tools {
		if tool.Name == "save_memory" {
			t.Fatalf("save_memory offered in team-shared chat: %+v", calls[0].Tools)
		}
	}

	var tasksOut []chat.TaskItem
	var agentsOut []chat.AgentItem
	for _, msg := range calls[1].Messages {
		if msg.Role != "tool" {
			continue
		}
		switch msg.ToolName {
		case "list_tasks":
			if err := json.Unmarshal([]byte(msg.Text), &tasksOut); err != nil {
				t.Fatalf("list_tasks output = %q (%v)", msg.Text, err)
			}
		case "list_agents":
			if err := json.Unmarshal([]byte(msg.Text), &agentsOut); err != nil {
				t.Fatalf("list_agents output = %q (%v)", msg.Text, err)
			}
		}
	}

	if len(tasksOut) != 1 || tasksOut[0].ID != taskA.ID || tasksOut[0].Title != "Team A task" {
		t.Fatalf("list_tasks in Team A chat = %+v, want only Team A task %s (must exclude Team B %s and cross-shared %s)",
			tasksOut, taskA.ID, taskB.ID, taskShared.ID)
	}
	if len(agentsOut) != 1 || agentsOut[0].ID != agentA.ID || agentsOut[0].Name != "Team A Agent" {
		t.Fatalf("list_agents in Team A chat = %+v, want only enabled Team A agent %s (must exclude Team B %s and disabled)",
			agentsOut, agentA.ID, agentB.ID)
	}

	// Switch the chat to visibility = 'everyone': team-private items from both
	// Team A and Team B must be excluded.
	if _, err := r.owner.Exec(ctx, `UPDATE chat.chats SET visibility = 'everyone', visibility_team_id = NULL WHERE id = $1::uuid`, chatID); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerFull, chatID, "list in everyone chat", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(call int, s *frames) {
		if call == 3 {
			s.doneCalls("m1", 1, 1, nil,
				stubTool{ID: "t2", Name: "list_tasks", Args: `{}`},
				stubTool{ID: "a2", Name: "list_agents", Args: `{}`},
			)
			return
		}
		s.delta("listed everyone")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	calls = r.stub.seen()
	lastMsgs := calls[len(calls)-1].Messages
	var everyoneTasks []chat.TaskItem
	var everyoneAgents []chat.AgentItem
	for _, msg := range lastMsgs {
		if msg.Role != "tool" {
			continue
		}
		switch msg.ToolID {
		case "t2":
			if err := json.Unmarshal([]byte(msg.Text), &everyoneTasks); err != nil {
				t.Fatalf("everyone list_tasks output = %q (%v)", msg.Text, err)
			}
		case "a2":
			if err := json.Unmarshal([]byte(msg.Text), &everyoneAgents); err != nil {
				t.Fatalf("everyone list_agents output = %q (%v)", msg.Text, err)
			}
		}
	}
	if len(everyoneTasks) != 0 {
		t.Fatalf("everyone chat list_tasks leaked team tasks: %+v", everyoneTasks)
	}
	if len(everyoneAgents) != 0 {
		t.Fatalf("everyone chat list_agents leaked team agents: %+v", everyoneAgents)
	}
}

func TestMutatingUpmoToolsHoldByDefaultAndExecuteWithAllowance(t *testing.T) {
	r, tools := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()

	ownerFull := chat.Access{
		UserID: ownerUser,
		Allow:  "chat:create,chat:view,chat:edit,task:create,task:view,task:edit,agent:create,agent:view,agent:edit",
		Teams:  "{" + toolsTeamA + "}",
	}
	r.setAccess(ownerUser, ownerFull)

	agent, err := agents.CreateAgent(ctx, r.agents, nil, agents.Access{
		UserID: ownerUser,
		Allow:  ownerFull.Allow,
		Teams:  ownerFull.Teams,
	}, agents.AgentFields{
		Name:    "Ops",
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	chatID := r.open()

	// 1. Without an allowance, open_task holds and does not create a task row.
	if err := app.Say(ctx, r.store, ownerFull, chatID, "open a task", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) {
		s.doneCalls("m1", 1, 1, nil, stubTool{ID: "open1", Name: "open_task", Args: `{"goal":"Ship Q4 report"}`})
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	lines := r.lines(chatID)
	if len(lines) != 2 || lines[1].Status != "failed" || len(lines[1].Attempts) == 0 || lines[1].Attempts[len(lines[1].Attempts)-1].Class != "hold" {
		t.Fatalf("unallowed open_task reply = %+v, want failed with class hold", lines)
	}
	var taskCount int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks`).Scan(&taskCount); err != nil || taskCount != 0 {
		t.Fatalf("tasks after held open_task = %d (%v), want 0", taskCount, err)
	}

	// 2. Grant "Allow for this chat" for open_task and save_memory: both execute and persist.
	if err := r.store.AllowToolForChat(ctx, ownerFull, chatID, "open_task"); err != nil {
		t.Fatal(err)
	}
	if err := r.store.AllowToolForChat(ctx, ownerFull, chatID, "save_memory"); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerFull, chatID, "open and remember", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	callStart := len(r.stub.seen())
	r.stub.reply(func(call int, s *frames) {
		if call == callStart+1 {
			s.doneCalls("m1", 1, 1, nil,
				stubTool{ID: "open2", Name: "open_task", Args: `{"goal":"Ship Q4 report"}`},
				stubTool{ID: "mem1", Name: "save_memory", Args: `{"claim":"Prefers ISO dates"}`},
			)
			return
		}
		s.delta("done")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}

	var gotOwner, gotGoal, gotStatus string
	if err := r.owner.QueryRow(ctx, `SELECT owner_user_id::text, goal, status FROM tasks.tasks`).Scan(&gotOwner, &gotGoal, &gotStatus); err != nil {
		t.Fatalf("task row missing after allowed open_task: %v", err)
	}
	if gotOwner != ownerUser || gotGoal != "Ship Q4 report" || gotStatus != "Preparing" {
		t.Fatalf("task row = owner=%s goal=%q status=%q", gotOwner, gotGoal, gotStatus)
	}

	var factUser, factClaim, factTrust string
	if err := r.owner.QueryRow(ctx, `SELECT user_id::text, claim, trust FROM memory.facts`).Scan(&factUser, &factClaim, &factTrust); err != nil {
		t.Fatalf("memory fact missing after allowed save_memory: %v", err)
	}
	if factUser != ownerUser || factClaim != "Prefers ISO dates" || factTrust != "user" {
		t.Fatalf("fact row = user=%s claim=%q trust=%q", factUser, factClaim, factTrust)
	}
	var privateRead bool
	if err := r.owner.QueryRow(ctx, `SELECT private_read FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&privateRead); err != nil || !privateRead {
		t.Fatalf("chat.private_read = %v (%v), want true after save_memory", privateRead, err)
	}

	// 3. Even with an allowance row manually inserted for create_automation,
	// calling create_automation still holds and creates neither a template nor an automation.
	if err := r.store.AllowToolForChat(ctx, ownerFull, chatID, "create_automation"); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerFull, chatID, "create an automation", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) {
		s.doneCalls("m1", 1, 1, nil, stubTool{
			ID:   "auto1",
			Name: "create_automation",
			Args: fmt.Sprintf(`{"goal":"Weekly digest","agent_id":%q,"kind":"cron","spec":"0 9 * * 1","budget_cents":500}`, agent.ID),
		})
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	lines = r.lines(chatID)
	lastLine := lines[len(lines)-1]
	if lastLine.Status != "failed" || len(lastLine.Attempts) == 0 || lastLine.Attempts[len(lastLine.Attempts)-1].Class != "hold" {
		t.Fatalf("create_automation with allowance reply = %+v, want failed with class hold", lastLine)
	}
	var tplCount, autoCount int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.templates`).Scan(&tplCount); err != nil || tplCount != 0 {
		t.Fatalf("templates after held create_automation = %d (%v), want 0", tplCount, err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.automations`).Scan(&autoCount); err != nil || autoCount != 0 {
		t.Fatalf("automations after held create_automation = %d (%v), want 0", autoCount, err)
	}

	// 4. Verify CreateAutomation itself persists template + automation under narrowed RLS
	// once executed after approval.
	tplID, autoID, err := tools.CreateAutomation(ctx, ownerFull, chat.AccessScope{}, chat.AutomationInput{
		Goal:        "Weekly digest",
		AgentID:     agent.ID,
		Kind:        "cron",
		Spec:        "0 9 * * 1",
		BudgetCents: 500,
	})
	if err != nil || tplID == "" || autoID == "" {
		t.Fatalf("CreateAutomation = tpl=%q auto=%q err=%v", tplID, autoID, err)
	}

	// 5. Verify save_memory refuses if the chat becomes shared mid-round.
	chat2 := r.open()
	if err := r.store.AllowToolForChat(ctx, ownerFull, chat2, "save_memory"); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerFull, chat2, "remember this", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	callStart = len(r.stub.seen())
	r.stub.reply(func(call int, s *frames) {
		if call == callStart+1 {
			if _, err := r.owner.Exec(ctx, `
				INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
				VALUES ('chat', $1::uuid, $2::uuid, 'viewer', $3::uuid)`, chat2, otherUser, ownerUser); err != nil {
				t.Error(err)
				return
			}
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "mem2", Name: "save_memory", Args: `{"claim":"Should not save"}`})
			return
		}
		s.delta("refused")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	var toolBody string
	var isError bool
	if err := r.owner.QueryRow(ctx, `SELECT body, is_error FROM chat.turns WHERE chat_id = $1::uuid AND role = 'tool' AND tool_name = 'save_memory'`, chat2).Scan(&toolBody, &isError); err != nil {
		t.Fatal(err)
	}
	if !isError || !strings.Contains(toolBody, "private tool unavailable in shared chat") {
		t.Fatalf("save_memory mid-round share = body=%q isError=%v", toolBody, isError)
	}
	var unsaved int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM memory.facts WHERE claim = 'Should not save'`).Scan(&unsaved); err != nil || unsaved != 0 {
		t.Fatalf("unsaved fact count = %d (%v), want 0", unsaved, err)
	}
}
