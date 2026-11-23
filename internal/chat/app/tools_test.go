package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

type spyExecutor struct {
	calls int
	res   agentloop.ToolResult
	err   error
}

func (s *spyExecutor) Execute(_ context.Context, _ *agentloop.RunState, _ agentloop.ToolCall) (agentloop.ToolResult, error) {
	s.calls++
	return s.res, s.err
}

type stubModel struct {
	resp agentloop.PromptResponse
}

func (m stubModel) Complete(_ context.Context, _ agentloop.PromptRequest) (agentloop.PromptResponse, error) {
	return m.resp, nil
}

func TestNeverFromChatListAndValidateChatToolBlockAllForbiddenDomains(t *testing.T) {
	// 1. Every prefix and exact name across all 4 forbidden domains is rejected
	// by both IsNeverFromChat and ValidateChatTool, even if disguised as a connector.
	for _, prefix := range domain.ForbiddenChatToolPrefixes {
		name := prefix + "do"
		if !domain.IsNeverFromChat(name) {
			t.Errorf("IsNeverFromChat(%q) = false, want true", name)
		}
		for _, def := range []agentloop.ToolDef{
			{Name: name, Category: agentloop.CategoryDirect, Op: agentloop.OpRead},
			{Name: strings.ToUpper(name), Category: agentloop.CategoryConnector, Op: agentloop.OpChange, External: true, Connector: "admin"},
		} {
			if err := ValidateChatTool(def); !errors.Is(err, domain.ErrForbiddenChatTool) {
				t.Errorf("ValidateChatTool(%+v) = %v, want ErrForbiddenChatTool", def, err)
			}
		}
	}
	for name := range domain.ForbiddenChatToolNames {
		if !domain.IsNeverFromChat(name) {
			t.Errorf("IsNeverFromChat(%q) = false, want true", name)
		}
		for _, def := range []agentloop.ToolDef{
			{Name: name, Category: agentloop.CategoryDirect, Op: agentloop.OpChange},
			{Name: " " + strings.ToUpper(name) + " ", Category: agentloop.CategoryConnector, Op: agentloop.OpDelete, External: true, Connector: "admin"},
		} {
			if err := ValidateChatTool(def); !errors.Is(err, domain.ErrForbiddenChatTool) {
				t.Errorf("ValidateChatTool(%+v) = %v, want ErrForbiddenChatTool", def, err)
			}
		}
	}

	// 2. Non-allowlisted direct tools, sandbox tools, invalid ops, and malformed
	// connector tools are rejected.
	rejected := []struct {
		label string
		def   agentloop.ToolDef
	}{
		{"empty name", agentloop.ToolDef{Name: "", Category: agentloop.CategoryDirect, Op: agentloop.OpRead}},
		{"invalid op", agentloop.ToolDef{Name: "list_tasks", Category: agentloop.CategoryDirect, Op: agentloop.ToolOp("admin")}},
		{"empty op", agentloop.ToolDef{Name: "list_tasks", Category: agentloop.CategoryDirect}},
		{"direct not in allowlist", agentloop.ToolDef{Name: "save_output", Category: agentloop.CategoryDirect, Op: agentloop.OpChange}},
		{"empty category not in allowlist", agentloop.ToolDef{Name: "arbitrary_tool", Op: agentloop.OpRead}},
		{"direct marked external", agentloop.ToolDef{Name: "list_tasks", Category: agentloop.CategoryDirect, Op: agentloop.OpRead, External: true}},
		{"sandbox shell", agentloop.ToolDef{Name: "shell", Category: agentloop.CategorySandbox, Op: agentloop.OpChange}},
		{"sandbox read_file", agentloop.ToolDef{Name: "read_file", Category: agentloop.CategorySandbox, Op: agentloop.OpRead}},
		{"sandbox write_file", agentloop.ToolDef{Name: "write_file", Category: agentloop.CategorySandbox, Op: agentloop.OpChange}},
		{"unknown category", agentloop.ToolDef{Name: "list_tasks", Category: agentloop.ToolCategory("internal"), Op: agentloop.OpRead}},
		{"connector not external", agentloop.ToolDef{Name: "github_list_issues", Category: agentloop.CategoryConnector, Op: agentloop.OpRead, External: false, Connector: "github"}},
		{"connector empty name", agentloop.ToolDef{Name: "github_list_issues", Category: agentloop.CategoryConnector, Op: agentloop.OpRead, External: true, Connector: "   "}},
	}
	for _, tc := range rejected {
		if err := ValidateChatTool(tc.def); !errors.Is(err, domain.ErrForbiddenChatTool) {
			t.Errorf("%s: ValidateChatTool(%+v) = %v, want ErrForbiddenChatTool", tc.label, tc.def, err)
		}
	}

	validConnector := agentloop.ToolDef{
		Name:      "github_list_issues",
		Category:  agentloop.CategoryConnector,
		Op:        agentloop.OpRead,
		External:  true,
		Connector: "github",
	}
	if err := ValidateChatTool(validConnector); err != nil {
		t.Fatalf("valid connector tool rejected: %v", err)
	}

	// 3. NewChatCatalog rejects any invalid tool in initial or discoverable.
	badTool := agentloop.ToolDef{Name: "invite_member", Category: agentloop.CategoryDirect, Op: agentloop.OpChange}
	sandboxTool := agentloop.ToolDef{Name: "shell", Category: agentloop.CategorySandbox, Op: agentloop.OpChange}
	if _, err := NewChatCatalog([]agentloop.ToolDef{badTool}, nil); !errors.Is(err, domain.ErrForbiddenChatTool) {
		t.Fatalf("NewChatCatalog(bad initial) = %v, want ErrForbiddenChatTool", err)
	}
	if _, err := NewChatCatalog(nil, []agentloop.ToolDef{badTool}); !errors.Is(err, domain.ErrForbiddenChatTool) {
		t.Fatalf("NewChatCatalog(bad discoverable) = %v, want ErrForbiddenChatTool", err)
	}
	if _, err := NewChatCatalog(nil, []agentloop.ToolDef{sandboxTool}); !errors.Is(err, domain.ErrForbiddenChatTool) {
		t.Fatalf("NewChatCatalog(sandbox discoverable) = %v, want ErrForbiddenChatTool", err)
	}
	if _, err := NewChatCatalog([]agentloop.ToolDef{listTasksTool}, []agentloop.ToolDef{validConnector}); err != nil {
		t.Fatalf("NewChatCatalog(valid) = %v", err)
	}

	// 4. timedExecutor.Execute refuses forbidden calls without invoking inner.Execute,
	// both with a catalog and with a nil catalog.
	t.Run("executor blocks forbidden tools", func(t *testing.T) {
		cat := agentloop.NewToolCatalog([]agentloop.ToolDef{badTool, sandboxTool}, nil)
		for _, callName := range []string{"invite_member", "audit_export", "shell", "unregistered_tool"} {
			spy := &spyExecutor{}
			exec := timedExecutor{inner: spy, limit: time.Second}
			res, err := exec.Execute(context.Background(), &agentloop.RunState{Catalog: cat}, agentloop.ToolCall{Name: callName})
			if !errors.Is(err, domain.ErrForbiddenChatTool) || !res.IsError {
				t.Errorf("call %q: got res=%+v err=%v, want ErrForbiddenChatTool", callName, res, err)
			}
			if spy.calls != 0 {
				t.Errorf("call %q invoked inner.Execute %d times, want 0", callName, spy.calls)
			}
		}
		for _, callName := range []string{"invite_member", "audit_export"} {
			spy := &spyExecutor{}
			exec := timedExecutor{inner: spy, limit: time.Second}
			res, err := exec.Execute(context.Background(), &agentloop.RunState{}, agentloop.ToolCall{Name: callName})
			if !errors.Is(err, domain.ErrForbiddenChatTool) || !res.IsError {
				t.Errorf("nil catalog call %q: got res=%+v err=%v, want ErrForbiddenChatTool", callName, res, err)
			}
			if spy.calls != 0 {
				t.Errorf("nil catalog call %q invoked inner.Execute %d times, want 0", callName, spy.calls)
			}
		}
	})

	// 5. timedExecutor.Execute filters res.DiscoveredTools through ValidateChatTool
	// before Engine.finalizeToolResult can merge them into run.Catalog.
	t.Run("executor filters find_tool discovered tools before merge", func(t *testing.T) {
		unallowedDirect := agentloop.ToolDef{Name: "custom_admin", Category: agentloop.CategoryDirect, Op: agentloop.OpChange}
		discovered := []agentloop.ToolDef{validConnector, badTool, sandboxTool, unallowedDirect}
		rawAll, _ := json.Marshal(discovered)
		spy := &spyExecutor{res: agentloop.ToolResult{Output: string(rawAll), DiscoveredTools: discovered}}
		cat, err := NewChatCatalog(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		exec := timedExecutor{inner: spy, limit: time.Second}
		run := &agentloop.RunState{RunID: "r1", ObjectID: "c1", ObjectType: "chat", UserID: "u1", Catalog: cat}
		engine := agentloop.Engine{
			Model: stubModel{resp: agentloop.PromptResponse{
				Model: "m1",
				ToolCalls: []agentloop.ToolCall{
					{ID: "call1", Name: "find_tool", Arguments: json.RawMessage(`{"query":"all"}`)},
				},
			}},
			Executor: exec,
		}
		outcome, err := engine.RunTurn(context.Background(), run, []agentloop.Message{{Role: agentloop.RoleUser, Text: "hi"}}, "")
		if err != nil {
			t.Fatalf("RunTurn err = %v", err)
		}
		if _, ok := run.Catalog.Get(validConnector.Name); !ok {
			t.Fatalf("valid discovered connector tool was not merged into catalog")
		}
		for _, blocked := range []string{badTool.Name, sandboxTool.Name, unallowedDirect.Name} {
			if _, ok := run.Catalog.Get(blocked); ok {
				t.Fatalf("forbidden discovered tool %q leaked into run.Catalog", blocked)
			}
		}
		lastMsg := outcome.Appended[len(outcome.Appended)-1]
		var outTools []agentloop.ToolDef
		if err := json.Unmarshal([]byte(lastMsg.Text), &outTools); err != nil {
			t.Fatalf("find_tool output not JSON: %q (%v)", lastMsg.Text, err)
		}
		if len(outTools) != 1 || outTools[0].Name != validConnector.Name {
			t.Fatalf("find_tool filtered output = %+v, want [%s]", outTools, validConnector.Name)
		}
	})

	// 6. Every tool in the full production chat catalog passes ValidateChatTool.
	prodCat, err := catalog(&Agent{ID: "a1"}, true, true, true, true)
	if err != nil {
		t.Fatalf("production catalog error: %v", err)
	}
	wantTools := []string{
		"retrieve_output", "find_tool", "open_lesson",
		"search_memory", "search_knowledge",
		"list_tasks", "open_task", "save_memory", "list_agents", "create_automation",
	}
	gotTools := prodCat.PromptTools()
	if len(gotTools) != len(wantTools) {
		t.Fatalf("production catalog has %d tools (%+v), want %d", len(gotTools), gotTools, len(wantTools))
	}
	for _, def := range gotTools {
		if err := ValidateChatTool(def); err != nil {
			t.Errorf("production tool %q failed ValidateChatTool: %v", def.Name, err)
		}
	}
	for _, name := range wantTools {
		if _, ok := prodCat.Get(name); !ok {
			t.Errorf("production catalog missing %q", name)
		}
	}
}

func TestNarrowTeamLiteralHardInvariant(t *testing.T) {
	const (
		teamA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa1"
		teamB = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa2"
	)
	bothTeams := "{" + teamA + "," + teamB + "}"

	cases := []struct {
		name      string
		scope     agentloop.AccessScope
		liveTeams string
		want      string
	}{
		{
			name:      "private preserves author teams when claim.Scope.UserTeams is empty",
			scope:     agentloop.AccessScope{ObjectVisibility: agentloop.VisibilityPrivate},
			liveTeams: bothTeams,
			want:      bothTeams,
		},
		{
			name:      "team intersects author teams with chat visibility_team_id",
			scope:     agentloop.AccessScope{ObjectVisibility: agentloop.VisibilityTeam, VisibilityTeamID: teamA},
			liveTeams: bothTeams,
			want:      "{" + teamA + "}",
		},
		{
			name:      "team returns empty when author is no longer in visibility_team_id",
			scope:     agentloop.AccessScope{ObjectVisibility: agentloop.VisibilityTeam, VisibilityTeamID: teamA},
			liveTeams: "{" + teamB + "}",
			want:      "{}",
		},
		{
			name:      "team returns empty when visibility_team_id is empty",
			scope:     agentloop.AccessScope{ObjectVisibility: agentloop.VisibilityTeam},
			liveTeams: bothTeams,
			want:      "{}",
		},
		{
			name:      "everyone always returns empty regardless of author teams",
			scope:     agentloop.AccessScope{ObjectVisibility: agentloop.VisibilityEveryone},
			liveTeams: bothTeams,
			want:      "{}",
		},
		{
			name:      "empty live teams stays empty",
			scope:     agentloop.AccessScope{ObjectVisibility: agentloop.VisibilityPrivate},
			liveTeams: "{}",
			want:      "{}",
		},
		{
			name:      "blank live teams stays empty",
			scope:     agentloop.AccessScope{ObjectVisibility: agentloop.VisibilityPrivate},
			liveTeams: "",
			want:      "{}",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NarrowTeamLiteral(tc.scope, tc.liveTeams); got != tc.want {
				t.Fatalf("NarrowTeamLiteral(%+v, %q) = %q, want %q", tc.scope, tc.liveTeams, got, tc.want)
			}
		})
	}

	// narrowChatAccess always strips SharedView and SharedEdit to "{}".
	in := Access{
		UserID:     "u1",
		Allow:      "chat:view,chat:edit",
		Teams:      bothTeams,
		SharedView: "{018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbb1}",
		SharedEdit: "{018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbb2}",
	}
	got := narrowChatAccess(in, agentloop.AccessScope{
		ObjectVisibility: agentloop.VisibilityTeam,
		VisibilityTeamID: teamA,
	})
	if got.Teams != "{"+teamA+"}" || got.SharedView != "{}" || got.SharedEdit != "{}" {
		t.Fatalf("narrowChatAccess = %+v, want Teams={%s} SharedView={} SharedEdit={}", got, teamA)
	}
}
