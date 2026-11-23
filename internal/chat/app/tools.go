package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/trustfence"
)

const (
	defaultTaskListLimit = 20
	maxTaskListLimit     = 50
	maxTaskGoalRunes     = 2000
	maxMemoryClaimRunes  = 500
	maxAutoGoalRunes     = 500
	maxAutoSpecRunes     = 200
	maxAutoBudgetCents   = 100000
)

// NewTaskInput holds the arguments for opening a task from chat.
type NewTaskInput struct {
	Goal string `json:"goal"`
}

// TaskItem is one task summary returned to the chat model.
type TaskItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Phase     string `json:"phase"`
	UpdatedAt string `json:"updated_at"`
}

// AgentItem is one enabled agent summary returned to the chat model.
type AgentItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AutomationInput holds the arguments for creating a task template and automation from chat.
type AutomationInput struct {
	Goal        string `json:"goal"`
	AgentID     string `json:"agent_id"`
	Kind        string `json:"kind"`
	Spec        string `json:"spec"`
	BudgetCents int    `json:"budget_cents"`
}

// ActionTools executes the in-process Upmo tools exposed in chat.
type ActionTools interface {
	ListTasks(ctx context.Context, access Access, scope agentloop.AccessScope, limit int) ([]TaskItem, error)
	OpenTask(ctx context.Context, access Access, scope agentloop.AccessScope, in NewTaskInput) (TaskItem, error)
	SaveMemory(ctx context.Context, userID, claim string) (string, error)
	ListAgents(ctx context.Context, access Access, scope agentloop.AccessScope) ([]AgentItem, error)
	CreateAutomation(ctx context.Context, access Access, scope agentloop.AccessScope, in AutomationInput) (templateID, automationID string, err error)
}

var (
	listTasksTool = agentloop.ToolDef{
		Name:        "list_tasks",
		Description: "list tasks visible to the author in this chat's scope",
		Schema:      json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":50}},"additionalProperties":false}`),
		Category:    agentloop.CategoryDirect,
		Op:          agentloop.OpRead,
	}
	openTaskTool = agentloop.ToolDef{
		Name:        "open_task",
		Description: "open a new task in Preparing status for the author",
		Schema:      json.RawMessage(`{"type":"object","properties":{"goal":{"type":"string","minLength":1,"maxLength":2000}},"required":["goal"],"additionalProperties":false}`),
		Category:    agentloop.CategoryDirect,
		Op:          agentloop.OpChange,
	}
	saveMemoryTool = agentloop.ToolDef{
		Name:        "save_memory",
		Description: "save a personal user-confirmed fact to this person's memory",
		Schema:      json.RawMessage(`{"type":"object","properties":{"claim":{"type":"string","minLength":1,"maxLength":500}},"required":["claim"],"additionalProperties":false}`),
		Category:    agentloop.CategoryDirect,
		Op:          agentloop.OpChange,
	}
	listAgentsTool = agentloop.ToolDef{
		Name:        "list_agents",
		Description: "list enabled agents visible to the author in this chat's scope",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Category:    agentloop.CategoryDirect,
		Op:          agentloop.OpRead,
	}
	createAutomationTool = agentloop.ToolDef{
		Name:        "create_automation",
		Description: "create a task template and active automation trigger for the author",
		Schema:      json.RawMessage(`{"type":"object","properties":{"goal":{"type":"string","minLength":1,"maxLength":500},"agent_id":{"type":"string"},"kind":{"type":"string","enum":["cron","webhook","event"]},"spec":{"type":"string","minLength":1,"maxLength":200},"budget_cents":{"type":"integer","minimum":1,"maximum":100000}},"required":["goal","agent_id","kind","spec","budget_cents"],"additionalProperties":false}`),
		Category:    agentloop.CategoryDirect,
		Op:          agentloop.OpChange,
	}
)

// ValidateChatTool enforces the hard chat tool boundary:
//  1. Tool name must not be empty or match domain.IsNeverFromChat (prefix + exact-name blocklist across the 4 forbidden domains).
//  2. Op must be one of OpRead, OpChange, OpDelete.
//  3. CategoryDirect (or empty category) tools must be on the closed domain.AllowedDirectChatTools allowlist and have External == false.
//  4. CategoryConnector tools must have External == true and non-empty Connector; CategorySandbox is forbidden in chat.
func ValidateChatTool(def agentloop.ToolDef) error {
	name := strings.ToLower(strings.TrimSpace(def.Name))
	if domain.IsNeverFromChat(name) {
		return domain.ErrForbiddenChatTool
	}
	switch def.Op {
	case agentloop.OpRead, agentloop.OpChange, agentloop.OpDelete:
	default:
		return domain.ErrForbiddenChatTool
	}
	switch def.Category {
	case "", agentloop.CategoryDirect:
		if !domain.AllowedDirectChatTools[name] || def.External {
			return domain.ErrForbiddenChatTool
		}
	case agentloop.CategoryConnector:
		if !def.External || strings.TrimSpace(def.Connector) == "" {
			return domain.ErrForbiddenChatTool
		}
	default:
		// CategorySandbox and unknown categories are never allowed in chat.
		return domain.ErrForbiddenChatTool
	}
	return nil
}

// NarrowTeamLiteral computes the Postgres UUID array literal for upmo.teams
// on a cross-context tool call from chat.
//
// When scope.ObjectVisibility is VisibilityEveryone, it returns "{}" so an
// everyone chat never leaks team-private tasks, folders, or agents.
// When scope.ObjectVisibility is VisibilityTeam, it intersects the author's
// live teams with {scope.VisibilityTeamID}.
// When scope.ObjectVisibility is VisibilityPrivate, it preserves the author's
// live teams.
func NarrowTeamLiteral(scope agentloop.AccessScope, rawTeamsLiteral string) string {
	if scope.ObjectVisibility == agentloop.VisibilityEveryone {
		return "{}"
	}
	scope.UserTeams = parsePGUUIDArray(rawTeamsLiteral)
	scope.SharedView = ""
	scope.SharedEdit = ""
	return agentloop.NarrowRLS(scope).TeamsPGArray
}

func parsePGUUIDArray(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	if trimmed == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(trimmed, ",") {
		item := strings.TrimSpace(strings.Trim(strings.TrimSpace(part), `"`))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func narrowChatAccess(current Access, scope agentloop.AccessScope) Access {
	current.Teams = NarrowTeamLiteral(scope, current.Teams)
	current.SharedView = "{}"
	current.SharedEdit = "{}"
	return current
}

func decodeToolArgs(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data in tool arguments")
	}
	return nil
}

func directTools(w *replyWorker, claim Claim, live *liveAccess) map[string]agentloop.ToolHandler {
	if w.deps.Tools == nil {
		return nil
	}
	return map[string]agentloop.ToolHandler{
		"list_tasks": func(ctx context.Context, _ *agentloop.RunState, call agentloop.ToolCall) (agentloop.ToolResult, error) {
			current, ok := live.get()
			if !ok {
				return agentloop.ToolResult{Output: errRevoked.Error(), IsError: true}, errRevoked
			}
			var args struct {
				Limit *int `json:"limit"`
			}
			if err := decodeToolArgs(call.Arguments, &args); err != nil {
				return agentloop.ToolResult{Output: "invalid arguments", IsError: true}, err
			}
			limit := defaultTaskListLimit
			if args.Limit != nil {
				limit = *args.Limit
				if limit < 1 || limit > maxTaskListLimit {
					err := errors.New("limit must be between 1 and 50")
					return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
				}
			}
			narrowed := narrowChatAccess(current, claim.Scope)
			items, err := w.deps.Tools.ListTasks(ctx, narrowed, claim.Scope, limit)
			if err != nil {
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			if items == nil {
				items = []TaskItem{}
			}
			raw, err := json.Marshal(items)
			if err != nil {
				return agentloop.ToolResult{IsError: true}, err
			}
			return agentloop.ToolResult{Output: string(raw)}, nil
		},
		"open_task": func(ctx context.Context, _ *agentloop.RunState, call agentloop.ToolCall) (agentloop.ToolResult, error) {
			current, ok := live.get()
			if !ok {
				return agentloop.ToolResult{Output: errRevoked.Error(), IsError: true}, errRevoked
			}
			var args NewTaskInput
			if err := decodeToolArgs(call.Arguments, &args); err != nil {
				return agentloop.ToolResult{Output: "invalid arguments", IsError: true}, err
			}
			cleanGoal := strings.TrimSpace(trustfence.NeutralizeSlot(args.Goal))
			if cleanGoal == "" || utf8.RuneCountInString(cleanGoal) > maxTaskGoalRunes {
				err := errors.New("goal must be between 1 and 2000 characters")
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			narrowed := narrowChatAccess(current, claim.Scope)
			created, err := w.deps.Tools.OpenTask(ctx, narrowed, claim.Scope, NewTaskInput{Goal: cleanGoal})
			if err != nil {
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			raw, err := json.Marshal(struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Goal   string `json:"goal"`
			}{
				ID:     created.ID,
				Status: created.Status,
				Goal:   created.Title,
			})
			if err != nil {
				return agentloop.ToolResult{IsError: true}, err
			}
			return agentloop.ToolResult{Output: string(raw)}, nil
		},
		"save_memory": func(ctx context.Context, _ *agentloop.RunState, call agentloop.ToolCall) (agentloop.ToolResult, error) {
			current, ok := live.get()
			if !ok {
				return agentloop.ToolResult{Output: errRevoked.Error(), IsError: true}, errRevoked
			}
			private, err := w.deps.Rows.IsPrivate(ctx, current, claim.Job.ChatID)
			if err != nil {
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			if !private {
				err := errors.New("private tool unavailable in shared chat")
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			var args struct {
				Claim string `json:"claim"`
			}
			if err := decodeToolArgs(call.Arguments, &args); err != nil {
				return agentloop.ToolResult{Output: "invalid arguments", IsError: true}, err
			}
			claimText := strings.TrimSpace(args.Claim)
			if claimText == "" || utf8.RuneCountInString(claimText) > maxMemoryClaimRunes {
				err := errors.New("claim must be between 1 and 500 characters")
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			id, err := w.deps.Tools.SaveMemory(ctx, current.UserID, claimText)
			if err != nil {
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			raw, err := json.Marshal(struct {
				ID    string `json:"id"`
				Claim string `json:"claim"`
			}{
				ID:    id,
				Claim: claimText,
			})
			if err != nil {
				return agentloop.ToolResult{IsError: true}, err
			}
			return agentloop.ToolResult{Output: string(raw), Private: true}, nil
		},
		"list_agents": func(ctx context.Context, _ *agentloop.RunState, call agentloop.ToolCall) (agentloop.ToolResult, error) {
			current, ok := live.get()
			if !ok {
				return agentloop.ToolResult{Output: errRevoked.Error(), IsError: true}, errRevoked
			}
			var args struct{}
			if err := decodeToolArgs(call.Arguments, &args); err != nil {
				return agentloop.ToolResult{Output: "invalid arguments", IsError: true}, err
			}
			narrowed := narrowChatAccess(current, claim.Scope)
			items, err := w.deps.Tools.ListAgents(ctx, narrowed, claim.Scope)
			if err != nil {
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			if items == nil {
				items = []AgentItem{}
			}
			raw, err := json.Marshal(items)
			if err != nil {
				return agentloop.ToolResult{IsError: true}, err
			}
			return agentloop.ToolResult{Output: string(raw)}, nil
		},
		"create_automation": func(ctx context.Context, _ *agentloop.RunState, call agentloop.ToolCall) (agentloop.ToolResult, error) {
			current, ok := live.get()
			if !ok {
				return agentloop.ToolResult{Output: errRevoked.Error(), IsError: true}, errRevoked
			}
			var args AutomationInput
			if err := decodeToolArgs(call.Arguments, &args); err != nil {
				return agentloop.ToolResult{Output: "invalid arguments", IsError: true}, err
			}
			args.Goal = strings.TrimSpace(trustfence.NeutralizeSlot(args.Goal))
			args.AgentID = strings.TrimSpace(args.AgentID)
			args.Kind = strings.TrimSpace(args.Kind)
			args.Spec = strings.TrimSpace(args.Spec)
			if args.Goal == "" || utf8.RuneCountInString(args.Goal) > maxAutoGoalRunes {
				err := errors.New("goal must be between 1 and 500 characters")
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			if args.AgentID == "" {
				err := errors.New("agent_id is required")
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			switch args.Kind {
			case "cron", "webhook", "event":
			default:
				err := errors.New("kind must be cron, webhook, or event")
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			if args.Spec == "" || utf8.RuneCountInString(args.Spec) > maxAutoSpecRunes {
				err := errors.New("spec must be between 1 and 200 characters")
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			if args.BudgetCents < 1 || args.BudgetCents > maxAutoBudgetCents {
				err := errors.New("budget_cents must be between 1 and 100000")
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			narrowed := narrowChatAccess(current, claim.Scope)
			templateID, automationID, err := w.deps.Tools.CreateAutomation(ctx, narrowed, claim.Scope, args)
			if err != nil {
				return agentloop.ToolResult{Output: err.Error(), IsError: true}, err
			}
			raw, err := json.Marshal(struct {
				TemplateID   string `json:"template_id"`
				AutomationID string `json:"automation_id"`
			}{
				TemplateID:   templateID,
				AutomationID: automationID,
			})
			if err != nil {
				return agentloop.ToolResult{IsError: true}, err
			}
			return agentloop.ToolResult{Output: string(raw)}, nil
		},
	}
}
