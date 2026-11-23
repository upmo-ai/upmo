//go:build integration

package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
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

	// 1. Without an allowance, open_task pauses for its author to approve.
	if err := app.Say(ctx, r.store, ownerFull, chatID, "open a task", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "open1", Name: "open_task", Args: `{"goal":"Prepare Q4 deck"}`})
			return
		}
		s.delta("created")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	lines := r.lines(chatID)
	if len(lines) != 1 || r.runRow(r.lastJob().Args.ReplyID).State != "waiting" {
		t.Fatalf("unallowed open_task lines = %+v run = %+v, want waiting", lines, r.runRow(r.lastJob().Args.ReplyID))
	}
	var taskCount int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks`).Scan(&taskCount); err != nil || taskCount != 0 {
		t.Fatalf("tasks after held open_task = %d (%v), want 0", taskCount, err)
	}
	var holdID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	if err := app.ApproveHold(ctx, r.store, ownerFull, chatID, holdID, true); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(chatID); len(got) != 2 || got[1].Status != "done" || got[1].Body != "created" {
		t.Fatalf("approved hold reply = %+v", got)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE goal = 'Prepare Q4 deck'`).Scan(&taskCount); err != nil || taskCount != 1 {
		t.Fatalf("approved task count = %d (%v)", taskCount, err)
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
	if err := r.owner.QueryRow(ctx, `SELECT owner_user_id::text, goal, status FROM tasks.tasks WHERE goal = 'Ship Q4 report'`).Scan(&gotOwner, &gotGoal, &gotStatus); err != nil {
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
	if r.runRow(r.lastJob().Args.ReplyID).State != "waiting" {
		t.Fatalf("create_automation with allowance run = %+v, want waiting", r.runRow(r.lastJob().Args.ReplyID))
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

func TestTaintedHoldApprovalDoesNotCreateAllowance(t *testing.T) {
	r, _ := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()
	ownerFull := chat.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit,task:create,task:view,task:edit", Teams: "{" + toolsTeamA + "}"}
	r.setAccess(ownerUser, ownerFull)
	chatID := r.open()
	if err := app.Say(ctx, r.store, ownerFull, chatID, "repeat this task", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	callStart := len(r.stub.seen())
	r.stub.reply(func(call int, s *frames) {
		switch call {
		case callStart + 1, callStart + 2:
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: fmt.Sprintf("task%d", call-callStart), Name: "open_task", Args: `{"goal":"Tainted approval task"}`})
		default:
			s.delta("done")
			s.done("m1", 1, 1, nil)
		}
	})
	firstJob := r.lastJob()
	if err := r.run(firstJob, 1); err != nil {
		t.Fatal(err)
	}
	var holdID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `UPDATE chat.chats SET tainted = true WHERE id = $1::uuid`, chatID); err != nil {
		t.Fatal(err)
	}
	if err := app.ApproveHold(ctx, r.store, ownerFull, chatID, holdID, true); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	var allowanceCount, taskCount, pendingCount int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.tool_allowances WHERE chat_id = $1::uuid AND user_id = $2::uuid AND tool = 'open_task'`, chatID, ownerUser).Scan(&allowanceCount); err != nil || allowanceCount != 0 {
		t.Fatalf("tainted open_task allowances = %d (%v), want 0", allowanceCount, err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE goal = 'Tainted approval task'`).Scan(&taskCount); err != nil || taskCount != 1 {
		t.Fatalf("executed tasks = %d (%v), want 1", taskCount, err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&pendingCount); err != nil || pendingCount != 1 {
		t.Fatalf("pending identical-call holds = %d (%v), want 1", pendingCount, err)
	}
	if row := r.runRow(firstJob.Args.ReplyID); row.State != "waiting" {
		t.Fatalf("run after repeated call = %+v, want waiting", row)
	}
}

func TestCreateAutomationHoldApprovalDoesNotCreateAllowance(t *testing.T) {
	r, _ := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()
	ownerFull := chat.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit,task:create,task:view,task:edit,agent:create,agent:view,agent:edit", Teams: "{" + toolsTeamA + "}"}
	r.setAccess(ownerUser, ownerFull)
	agent, err := agents.CreateAgent(ctx, r.agents, nil, agents.Access{UserID: ownerUser, Allow: ownerFull.Allow, Teams: ownerFull.Teams}, agents.AgentFields{Name: "Ops", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	chatID := r.open()
	if err := app.Say(ctx, r.store, ownerFull, chatID, "repeat this automation", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	args := fmt.Sprintf(`{"goal":"Weekly digest","agent_id":%q,"kind":"cron","spec":"0 9 * * 1","budget_cents":500}`, agent.ID)
	callStart := len(r.stub.seen())
	r.stub.reply(func(call int, s *frames) {
		switch call {
		case callStart + 1, callStart + 2:
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: fmt.Sprintf("automation%d", call-callStart), Name: "create_automation", Args: args})
		default:
			s.delta("done")
			s.done("m1", 1, 1, nil)
		}
	})
	firstJob := r.lastJob()
	if err := r.run(firstJob, 1); err != nil {
		t.Fatal(err)
	}
	var holdID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	if err := app.ApproveHold(ctx, r.store, ownerFull, chatID, holdID, true); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	var allowanceCount, templateCount, automationCount, pendingCount int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.tool_allowances WHERE chat_id = $1::uuid AND user_id = $2::uuid AND tool = 'create_automation'`, chatID, ownerUser).Scan(&allowanceCount); err != nil || allowanceCount != 0 {
		t.Fatalf("create_automation allowances = %d (%v), want 0", allowanceCount, err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.templates WHERE goal = 'Weekly digest'`).Scan(&templateCount); err != nil || templateCount != 1 {
		t.Fatalf("created templates = %d (%v), want 1", templateCount, err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.automations`).Scan(&automationCount); err != nil || automationCount != 1 {
		t.Fatalf("created automations = %d (%v), want 1", automationCount, err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&pendingCount); err != nil || pendingCount != 1 {
		t.Fatalf("pending identical-call holds = %d (%v), want 1", pendingCount, err)
	}
	if row := r.runRow(firstJob.Args.ReplyID); row.State != "waiting" {
		t.Fatalf("run after repeated call = %+v, want waiting", row)
	}
}

func TestWaitingRunSurvivesActiveEditorUserShare(t *testing.T) {
	r, ownerFull, chatID, replyID := waitingRunThroughShare(t, false)
	if err := app.Say(context.Background(), r.store, ownerFull, chatID, "owner says", app.Picks{}); !errors.Is(err, app.ErrBusy) {
		t.Fatalf("owner Say while user-shared run waits = %v, want ErrBusy", err)
	}
	if row := r.runRow(replyID); row.State != "waiting" {
		t.Fatalf("user-shared run after owner Say = %+v, want waiting", row)
	}
}

func TestWaitingRunSurvivesActiveEditorRoleShare(t *testing.T) {
	r, ownerFull, chatID, replyID := waitingRunThroughShare(t, true)
	if err := app.Say(context.Background(), r.store, ownerFull, chatID, "owner says", app.Picks{}); !errors.Is(err, app.ErrBusy) {
		t.Fatalf("owner Say while role-shared run waits = %v, want ErrBusy", err)
	}
	if row := r.runRow(replyID); row.State != "waiting" {
		t.Fatalf("role-shared run after owner Say = %+v, want waiting", row)
	}
}

func waitingRunThroughShare(t *testing.T, roleShare bool) (*rig, chat.Access, string, string) {
	t.Helper()
	r, _ := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()
	ownerFull := chat.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit,task:create,task:view,task:edit", Teams: "{" + toolsTeamA + "}"}
	r.setAccess(ownerUser, ownerFull)
	chatID := r.open()
	if roleShare {
		_, err := r.owner.Exec(ctx, `INSERT INTO identity.shares (object_type, object_id, grantee_role_id, level, granted_by) VALUES ('chat', $1::uuid, (SELECT role_id FROM identity.users WHERE id = $2::uuid), 'editor', $3::uuid)`, chatID, otherUser, ownerUser)
		if err != nil {
			t.Fatal(err)
		}
	} else if _, err := r.owner.Exec(ctx, `INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by) VALUES ('chat', $1::uuid, $2::uuid, 'editor', $3::uuid)`, chatID, otherUser, ownerUser); err != nil {
		t.Fatal(err)
	}
	other := chat.Access{UserID: otherUser, Allow: "chat:view,chat:edit,task:create,task:view,task:edit", SharedView: "{" + chatID + "}", SharedEdit: "{" + chatID + "}"}
	r.setAccess(otherUser, other)
	if err := app.Say(ctx, r.store, other, chatID, "author line", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) {
		s.doneCalls("m1", 1, 1, nil, stubTool{ID: "shared", Name: "open_task", Args: `{"goal":"Shared author task"}`})
	})
	job := r.lastJob()
	if err := r.run(job, 1); err != nil {
		t.Fatal(err)
	}
	if row := r.runRow(job.Args.ReplyID); row.State != "waiting" {
		t.Fatalf("author run = %+v, want waiting", row)
	}
	return r, ownerFull, chatID, job.Args.ReplyID
}

func TestTeamVisibilityDoesNotPreserveWaitingRunAfterEditorShareRemoved(t *testing.T) {
	r, _ := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()
	ownerFull := chat.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit,task:create,task:view,task:edit", Teams: "{" + toolsTeamA + "}"}
	r.setAccess(ownerUser, ownerFull)
	chatID := r.open()
	if _, err := r.owner.Exec(ctx, `UPDATE chat.chats SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, chatID, toolsTeamA); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by) VALUES ('chat', $1::uuid, $2::uuid, 'editor', $3::uuid)`, chatID, otherUser, ownerUser); err != nil {
		t.Fatal(err)
	}
	other := chat.Access{UserID: otherUser, Allow: "chat:view,chat:edit,task:create,task:view,task:edit", Teams: "{" + toolsTeamA + "}", SharedView: "{" + chatID + "}", SharedEdit: "{" + chatID + "}"}
	r.setAccess(otherUser, other)
	if err := app.Say(ctx, r.store, other, chatID, "author line", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) {
		s.doneCalls("m1", 1, 1, nil, stubTool{ID: "team-share", Name: "open_task", Args: `{"goal":"Team share task"}`})
	})
	job := r.lastJob()
	if err := r.run(job, 1); err != nil {
		t.Fatal(err)
	}
	if row := r.runRow(job.Args.ReplyID); row.State != "waiting" {
		t.Fatalf("author run = %+v, want waiting", row)
	}
	if _, err := r.owner.Exec(ctx, `DELETE FROM identity.shares WHERE object_type = 'chat' AND object_id = $1::uuid AND user_id = $2::uuid`, chatID, otherUser); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerFull, chatID, "owner says", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if row := r.runRow(job.Args.ReplyID); row.State != "failed" || !row.HistoryNil {
		t.Fatalf("run after share removal = %+v, want failed with cleared history", row)
	}
	var failClass string
	if err := r.owner.QueryRow(ctx, `SELECT attempts -> 0 ->> 'class' FROM chat.lines WHERE id = $1::uuid`, job.Args.ReplyID).Scan(&failClass); err != nil || failClass != "access" {
		t.Fatalf("failed run class = %q: %v", failClass, err)
	}
}

func TestHoldApproveAndDenyLifecycleRedactedAuthorOnlyViewAnd24hExpiry(t *testing.T) {
	r, _ := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()
	ownerFull := chat.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit,task:create,task:view,task:edit", Teams: "{" + toolsTeamA + "}"}
	r.setAccess(ownerUser, ownerFull)
	chatID := r.open()
	if _, err := r.owner.Exec(ctx, `INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by) VALUES ('chat', $1::uuid, $2::uuid, 'editor', $3::uuid)`, chatID, otherUser, ownerUser); err != nil {
		t.Fatal(err)
	}
	other := chat.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: "{" + chatID + "}", SharedEdit: "{" + chatID + "}"}
	r.setAccess(otherUser, other)
	if err := app.Say(ctx, r.store, ownerFull, chatID, "prepare the deck", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c1", Name: "open_task", Args: `{"goal":"Prepare Q4 deck"}`})
			return
		}
		s.delta("done")
		s.done("m1", 1, 1, nil)
	})
	job := r.lastJob()
	if err := r.run(job, 1); err != nil {
		t.Fatal(err)
	}
	if row := r.runRow(job.Args.ReplyID); row.State != "waiting" {
		t.Fatalf("run = %+v", row)
	}
	var holdID string
	var holdTurnSeq int
	var blocked int
	if err := r.owner.QueryRow(ctx, `SELECT id::text, hold_turn_seq FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&holdID, &holdTurnSeq); err != nil || holdTurnSeq != 1 {
		t.Fatalf("hold = %s seq %d: %v", holdID, holdTurnSeq, err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.turns WHERE chat_id = $1::uuid AND role = 'blocked'`, chatID).Scan(&blocked); err != nil || blocked != 1 {
		t.Fatalf("blocked rows = %d: %v", blocked, err)
	}
	ownerMux, otherMux := r.httpMux(ownerFull), r.httpMux(other)
	get := func(mux *http.ServeMux) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats/"+chatID, nil))
		return rec
	}
	ownerView, otherView := get(ownerMux), get(otherMux)
	if ownerView.Code != http.StatusOK || !strings.Contains(ownerView.Body.String(), `"tool_name":"open_task"`) || strings.Contains(ownerView.Body.String(), `"arguments"`) {
		t.Fatalf("author view = %d %s", ownerView.Code, ownerView.Body.String())
	}
	if otherView.Code != http.StatusOK || !strings.Contains(otherView.Body.String(), `"hold":null`) {
		t.Fatalf("other view = %d %s", otherView.Code, otherView.Body.String())
	}
	post := func(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return rec
	}
	path := "/api/chats/" + chatID + "/holds/" + holdID + "/approve"
	if got := post(otherMux, path, `{}`); got.Code != http.StatusForbidden {
		t.Fatalf("other approve status = %d", got.Code)
	}
	if got := post(ownerMux, "/api/chats/"+chatID+"/holds/018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb/approve", `{}`); got.Code != http.StatusNotFound {
		t.Fatalf("missing hold status = %d", got.Code)
	}
	if err := app.Say(ctx, r.store, ownerFull, chatID, "another", app.Picks{}); !errors.Is(err, app.ErrBusy) {
		var holdState, holdReason string
		_ = r.owner.QueryRow(ctx, `SELECT state, reason FROM chat.holds WHERE id = $1::uuid`, holdID).Scan(&holdState, &holdReason)
		t.Fatalf("Say while waiting error = %v run=%+v hold=%s/%s", err, r.runRow(job.Args.ReplyID), holdState, holdReason)
	}
	if got := post(ownerMux, "/api/chats/"+chatID, `{"body":"another"}`); got.Code != http.StatusConflict {
		t.Fatalf("say while waiting status = %d body=%s", got.Code, got.Body.String())
	}
	if _, err := r.owner.Exec(ctx, `UPDATE chat.runs SET created_at = now() - interval '3 hours' WHERE id = $1::uuid`, job.Args.ReplyID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `UPDATE chat.deltas SET at = now() - interval '3 hours' WHERE line_id = $1::uuid`, job.Args.ReplyID); err != nil {
		t.Fatal(err)
	}
	if n, err := r.store.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("sweep = %d: %v", n, err)
	}
	if row := r.runRow(job.Args.ReplyID); row.State != "waiting" {
		t.Fatalf("sweep changed run: %+v", row)
	}
	if got := post(ownerMux, path, `{"allow_for_chat":true}`); got.Code != http.StatusAccepted {
		t.Fatalf("approve status = %d %s", got.Code, got.Body.String())
	}
	var allowance int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.tool_allowances WHERE chat_id = $1::uuid AND user_id = $2::uuid AND tool = 'open_task'`, chatID, ownerUser).Scan(&allowance); err != nil || allowance != 1 {
		t.Fatalf("allowance = %d: %v", allowance, err)
	}
	if got := post(ownerMux, path, `{}`); got.Code != http.StatusConflict {
		t.Fatalf("second approve status = %d", got.Code)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if lines := r.lines(chatID); len(lines) != 2 || lines[1].Status != "done" {
		t.Fatalf("lines = %+v", lines)
	}
	expireChat := r.open()
	if err := app.Say(ctx, r.store, ownerFull, expireChat, "another task", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) {
		s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c2", Name: "open_task", Args: `{"goal":"Expire me"}`})
	})
	expireJob := r.lastJob()
	if err := r.run(expireJob, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `UPDATE chat.holds SET created_at = now() - interval '25 hours' WHERE chat_id = $1::uuid AND state = 'pending'`, expireChat); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := r.owner.QueryRow(ctx, `SELECT state, reason FROM chat.holds WHERE chat_id = $1::uuid AND line_id = $2::uuid`, expireChat, expireJob.Args.ReplyID).Scan(&state, &reason); err != nil || state != "closed" || reason != "expired" {
		t.Fatalf("expired hold = %s/%s: %v", state, reason, err)
	}
	revokedChat := r.open()
	if _, err := r.owner.Exec(ctx, `INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by) VALUES ('chat', $1::uuid, $2::uuid, 'editor', $3::uuid)`, revokedChat, otherUser, ownerUser); err != nil {
		t.Fatal(err)
	}
	other.SharedView, other.SharedEdit = "{"+revokedChat+"}", "{"+revokedChat+"}"
	r.setAccess(otherUser, other)
	if err := app.Say(ctx, r.store, other, revokedChat, "author line", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) {
		s.doneCalls("m1", 1, 1, nil, stubTool{ID: "access", Name: "open_task", Args: `{"goal":"Author hold"}`})
	})
	revokedJob := r.lastJob()
	if err := r.run(revokedJob, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO chat.deltas (chat_id, line_id, seq, kind, body) VALUES ($1::uuid, $2::uuid, 1, 'text', repeat('x', 40000))`, revokedChat, revokedJob.Args.ReplyID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `DELETE FROM identity.shares WHERE object_type = 'chat' AND object_id = $1::uuid AND user_id = $2::uuid`, revokedChat, otherUser); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerFull, revokedChat, "continue", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT state FROM chat.holds WHERE chat_id = $1::uuid AND line_id = $2::uuid`, revokedChat, revokedJob.Args.ReplyID).Scan(&state); err != nil || state != "closed" {
		t.Fatalf("revoked hold state = %s: %v", state, err)
	}
	if row := r.runRow(revokedJob.Args.ReplyID); row.State != "failed" || !row.HistoryNil {
		t.Fatalf("revoked author run = %+v", row)
	}
	var failClass string
	if err := r.owner.QueryRow(ctx, `SELECT attempts -> 0 ->> 'class' FROM chat.lines WHERE id = $1::uuid`, revokedJob.Args.ReplyID).Scan(&failClass); err != nil || failClass != "access" {
		t.Fatalf("revoked author failure class = %q: %v", failClass, err)
	}
	var failedBodyRunes int
	if err := r.owner.QueryRow(ctx, `SELECT char_length(body) FROM chat.lines WHERE id = $1::uuid`, revokedJob.Args.ReplyID).Scan(&failedBodyRunes); err != nil || failedBodyRunes != 40000 {
		t.Fatalf("revoked author failed body length = %d: %v", failedBodyRunes, err)
	}
	deniedChat := r.open()
	if err := app.Say(ctx, r.store, ownerFull, deniedChat, "deny this", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) {
		s.doneCalls("m1", 1, 1, nil, stubTool{ID: "d1", Name: "open_task", Args: `{"goal":"Must not run"}`})
	})
	deniedJob := r.lastJob()
	if err := r.run(deniedJob, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, deniedChat).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	if got := post(ownerMux, "/api/chats/"+deniedChat+"/holds/"+holdID+"/deny", ""); got.Code != http.StatusAccepted {
		t.Fatalf("deny status = %d %s", got.Code, got.Body.String())
	}
	r.stub.reply(func(_ int, s *frames) { s.delta("denied"); s.done("m1", 1, 1, nil) })
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if got := r.stub.seen()[len(r.stub.seen())-1]; !containsToolResult(agentMessages(got), "d1", "open_task", "denied by user") {
		t.Fatalf("denied tool result absent: %+v", got.Messages)
	}
	var toolName, toolBody string
	var toolError bool
	if err := r.owner.QueryRow(ctx, `SELECT tool_name, body, is_error FROM chat.turns WHERE line_id = $1::uuid AND tool_id = 'd1' ORDER BY seq DESC LIMIT 1`, deniedJob.Args.ReplyID).Scan(&toolName, &toolBody, &toolError); err != nil || toolName != "open_task" || toolBody != "denied by user" || !toolError {
		t.Fatalf("denied turn = %s/%s/%v: %v", toolName, toolBody, toolError, err)
	}
}

func containsToolResult(messages []agentloop.Message, id, name, text string) bool {
	for _, message := range messages {
		if message.Role == agentloop.RoleTool && message.ToolID == id && message.ToolName == name && message.Text == text {
			return true
		}
	}
	return false
}

func TestApprovedCallRevalidationAndCrashRecoveryWithoutSecondDial(t *testing.T) {
	r, tools := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()
	ownerFull := chat.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit,task:create,task:view,task:edit", Teams: "{" + toolsTeamA + "}"}
	r.setAccess(ownerUser, ownerFull)
	chatID := r.open()
	if err := app.Say(ctx, r.store, ownerFull, chatID, "prepare the deck", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "bad", Name: "open_task", Args: `{"goal":"Must not run"}`})
			return
		}
		s.delta("blocked")
		s.done("m1", 1, 1, nil)
	})
	job := r.lastJob()
	if err := r.run(job, 1); err != nil {
		t.Fatal(err)
	}
	var holdID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `UPDATE chat.holds SET calls[1] = jsonb_set(calls[1], '{args_digest}', '"tampered"'::jsonb) WHERE id = $1::uuid`, holdID); err != nil {
		t.Fatal(err)
	}
	if err := app.ApproveHold(ctx, r.store, ownerFull, chatID, holdID, false); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	seen := r.stub.seen()
	resumedMessages := agentMessages(seen[len(seen)-1])
	if !containsToolResult(resumedMessages, "bad", "open_task", "blocked: argument digest mismatch") {
		t.Fatalf("tampered call result = %+v", resumedMessages)
	}
	var actualName, actualBody string
	var actualError bool
	if err := r.owner.QueryRow(ctx, `SELECT tool_name, body, is_error FROM chat.turns WHERE line_id = $1::uuid AND tool_id = 'bad' ORDER BY seq DESC LIMIT 1`, job.Args.ReplyID).Scan(&actualName, &actualBody, &actualError); err != nil || actualName != "open_task" || actualBody != "blocked: argument digest mismatch" || !actualError {
		t.Fatalf("revalidation turn = %s/%s/%v: %v", actualName, actualBody, actualError, err)
	}
	var count int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE goal = 'Must not run'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("tampered task count = %d: %v", count, err)
	}

	crashChat := r.open()
	if err := app.Say(ctx, r.store, ownerFull, crashChat, "make both tasks", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	callStart := len(r.stub.seen())
	r.stub.reply(func(call int, s *frames) {
		if call == callStart+1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c1", Name: "open_task", Args: `{"goal":"First task"}`}, stubTool{ID: "c2", Name: "open_task", Args: `{"goal":"Second task"}`})
			return
		}
		s.delta("done")
		s.done("m1", 1, 1, nil)
	})
	crashJob := r.lastJob()
	if err := r.run(crashJob, 1); err != nil {
		t.Fatal(err)
	}
	var seq int
	var crashHoldID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text, hold_turn_seq FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, crashChat).Scan(&crashHoldID, &seq); err != nil {
		t.Fatal(err)
	}
	if err := r.store.ApproveHold(ctx, ownerFull, crashChat, crashHoldID, false); err != nil {
		t.Fatal(err)
	}
	resumeJob := r.lastJob()
	claim, err := r.store.Claim(ctx, ownerFull, resumeJob.Args, "018f3b2c-7a10-7c4a-8e11-eeeeeeeeeeee")
	if err != nil || claim.DecidedHold == nil {
		t.Fatalf("resume claim = %+v: %v", claim, err)
	}
	scope := claim.Scope
	first, err := tools.OpenTask(ctx, ownerFull, scope, chat.NewTaskInput{Goal: "First task"})
	if err != nil {
		t.Fatal(err)
	}
	firstOutput, err := json.Marshal(map[string]string{"id": first.ID, "status": first.Status, "goal": "First task"})
	if err != nil {
		t.Fatal(err)
	}
	scopedID := agentloop.ScopedCallID(seq, "c1")
	log := r.store.ActionLog(scope)
	if err := log.BeginDial(ctx, agentloop.ActionRecord{CallID: scopedID, RunID: resumeJob.Args.ReplyID, ObjectID: crashChat, UserID: ownerUser, ToolName: "open_task", Action: "change open_task", Preview: "open_task()"}); err != nil {
		t.Fatal(err)
	}
	if err := log.FinishDial(ctx, resumeJob.Args.ReplyID, scopedID, agentloop.ActionStateDone, "cached", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SaveCachedToolOutput(ctx, scope, crashChat, resumeJob.Args.ReplyID, ownerUser, "open_task", scopedID, agentloop.ToolResult{Output: string(firstOutput)}); err != nil {
		t.Fatal(err)
	}
	if err := r.run(resumeJob, 2); err != nil {
		t.Fatal(err)
	}
	seen = r.stub.seen()
	resumedMessages = agentMessages(seen[len(seen)-1])
	if !containsToolResult(resumedMessages, "c1", "open_task", string(firstOutput)) {
		t.Fatalf("cached first result = %+v", resumedMessages)
	}
	if !hasToolMessage(resumedMessages, "c2", "open_task") {
		t.Fatalf("second tool result absent: %+v", resumedMessages)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE goal IN ('First task', 'Second task')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("crash recovery tasks = %d: %v", count, err)
	}
}

func hasToolMessage(messages []agentloop.Message, id, name string) bool {
	for _, message := range messages {
		if message.Role == agentloop.RoleTool && message.ToolID == id && message.ToolName == name {
			return true
		}
	}
	return false
}

func agentMessages(call stubCall) []agentloop.Message {
	out := make([]agentloop.Message, 0, len(call.Messages))
	for _, message := range call.Messages {
		out = append(out, agentloop.Message{Role: agentloop.MessageRole(message.Role), Text: message.Text, ToolName: message.ToolName, ToolID: message.ToolID})
	}
	return out
}

func TestHoldAtRound14ResumesOnRound15AndNeverRunsRound16(t *testing.T) {
	r, _ := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()
	ownerFull := chat.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit,task:create,task:view,task:edit", Teams: "{" + toolsTeamA + "}"}
	r.setAccess(ownerUser, ownerFull)
	chatID := r.open()
	if err := app.Say(ctx, r.store, ownerFull, chatID, "go to the fourteenth round", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	start := len(r.stub.seen())
	r.stub.reply(func(call int, s *frames) {
		round := call - start
		if round <= 13 {
			s.doneCalls("m1", 1, 0, nil, stubTool{ID: fmt.Sprintf("read%d", round), Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		if round == 14 {
			s.doneCalls("m1", 1, 0, nil, stubTool{ID: "held14", Name: "open_task", Args: `{"goal":"Round 14 task"}`})
			return
		}
		if round == 15 {
			s.doneCalls("m1", 1, 0, nil, stubTool{ID: "read15", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		t.Fatalf("unexpected model call %d", round)
	})
	job := r.lastJob()
	for round := 1; round <= 14; round++ {
		if err := r.run(job, 1); err != nil {
			t.Fatal(err)
		}
		if round < 14 {
			job = r.lastJob()
		}
	}
	var holdID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	if err := r.store.ApproveHold(ctx, ownerFull, chatID, holdID, false); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if jobs := r.jobs(); len(jobs) != 15 || jobs[len(jobs)-1].Args.Round != 15 {
		t.Fatalf("round jobs = %+v", jobs)
	}
	lines := r.lines(chatID)
	if lines[len(lines)-1].Status != "failed" || reasonOf(lines[len(lines)-1]) != "the reply took too many steps" {
		t.Fatalf("round 15 line = %+v", lines[len(lines)-1])
	}

	chat2 := r.open()
	if err := app.Say(ctx, r.store, ownerFull, chat2, "hold on round fifteen", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	start = len(r.stub.seen())
	r.stub.reply(func(call int, s *frames) {
		round := call - start
		if round <= 14 {
			s.doneCalls("m1", 1, 0, nil, stubTool{ID: fmt.Sprintf("r%d", round), Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		if round == 15 {
			s.doneCalls("m1", 1, 0, nil, stubTool{ID: "held15", Name: "open_task", Args: `{"goal":"Must not pause"}`})
			return
		}
		t.Fatalf("unexpected model call %d", round)
	})
	job = r.lastJob()
	for round := 1; round <= 15; round++ {
		if err := r.run(job, 1); err != nil {
			t.Fatal(err)
		}
		if round < 15 {
			job = r.lastJob()
		}
	}
	var count int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.holds WHERE chat_id = $1::uuid`, chat2).Scan(&count); err != nil || count != 0 {
		t.Fatalf("round 15 holds = %d: %v", count, err)
	}
	if row := r.runRow(job.Args.ReplyID); row.State != "failed" || !row.HistoryNil {
		t.Fatalf("round 15 hold run = %+v", row)
	}
	if len(r.jobs()) != 30 {
		t.Fatalf("round 15 hold added a job: %d", len(r.jobs()))
	}
}
