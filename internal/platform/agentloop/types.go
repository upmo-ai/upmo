// Package agentloop implements the shared turn engine, prompt cache prefix
// assembly, single-team RLS narrowing, exposure tracking, deterministic batch
// holds, dry-run simulation, and 3-transaction at-most-once execution used by
// tasks and chat. When Engine.ContextWindow > 0, RunTurn trims History with
// PostTurnBudget before tx3, so TurnOutcome.History and TurnCommit.History
// are the same trimmed history. Callers persist it and feed it back verbatim.
// With ContextWindow 0 nothing is trimmed. Trimming may save output before a
// turn later fails; retrying with the original history remains safe, though it
// can duplicate store writes. Trimming a retrieve_output page saves a second
// copy of content that is already stored. Several page reads in one turn all
// belong to the latest turn and cannot be trimmed, so a small window can
// return ErrContextExceeded. History must come from the server's own store,
// never client-supplied JSON, because the held_placeholder and offload flags
// are trusted.
package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/upmo-ai/upmo/internal/kernel"
)

// Breakpoint marks a prompt-cache prefix boundary.
type Breakpoint string

const (
	BreakTools  Breakpoint = "tools"
	BreakSystem Breakpoint = "system"
)

// ToolCategory classifies how a tool is dispatched.
type ToolCategory string

const (
	CategoryDirect    ToolCategory = "direct"
	CategorySandbox   ToolCategory = "sandbox"
	CategoryConnector ToolCategory = "connector"
)

// ToolOp classifies whether a tool reads or mutates state.
type ToolOp string

const (
	OpRead   ToolOp = "read"
	OpChange ToolOp = "change"
	OpDelete ToolOp = "delete"
)

// ToolDef describes one tool exposed to the model.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Category    ToolCategory    `json:"category,omitempty"`
	Op          ToolOp          `json:"op,omitempty"`
	External    bool            `json:"external,omitempty"`
	Connector   string          `json:"connector,omitempty"`
	ResourceArg string          `json:"resource_arg,omitempty"`
}

// ToolCall is a single tool invocation requested by the model.
type ToolCall struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
	IdempotencyKey   string          `json:"-"`
}

// MessageRole identifies the speaker of a message in a turn sequence.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// Message is one prompt message after the cached prefix.
type Message struct {
	Role            MessageRole `json:"role"`
	Text            string      `json:"text,omitempty"`
	ToolCalls       []ToolCall  `json:"tool_calls,omitempty"`
	ToolName        string      `json:"tool_name,omitempty"`
	ToolID          string      `json:"tool_id,omitempty"`
	IsError         bool        `json:"is_error,omitempty"`
	Offload         bool        `json:"offload,omitempty"`
	HeldPlaceholder bool        `json:"held_placeholder,omitempty"`
}

// ToolResult is the outcome of executing one tool call.
type ToolResult struct {
	Output          string
	IsError         bool
	Untrusted       bool
	Private         bool
	DiscoveredTools []ToolDef
	ResponseDigest  string
}

// ExposureState tracks monotonic taint and private-data exposure across turns.
type ExposureState struct {
	Tainted     bool `json:"tainted"`
	PrivateRead bool `json:"private_read"`
}

// Observe merges exposure flags from one tool execution into e.
func (e *ExposureState) Observe(untrusted, private bool) {
	if untrusted {
		e.Tainted = true
	}
	if private {
		e.PrivateRead = true
	}
}

// TimelineEventKind names the governance timeline event emitted by the loop.
type TimelineEventKind string

const (
	EventBlocked   TimelineEventKind = "blocked"
	EventHeld      TimelineEventKind = "held"
	EventSimulated TimelineEventKind = "simulated"
	EventExecuted  TimelineEventKind = "executed"
)

// TimelineEvent is a structured governance event for timelines and audit logs.
type TimelineEvent struct {
	Kind            TimelineEventKind `json:"kind"`
	CallID          string            `json:"call_id"`
	ToolName        string            `json:"tool_name"`
	Reason          string            `json:"reason,omitempty"`
	Action          string            `json:"action"`
	Preview         string            `json:"preview,omitempty"`
	UntrustedOrigin bool              `json:"untrusted_origin,omitempty"`
	DryRun          bool              `json:"dry_run,omitempty"`
	At              time.Time         `json:"at"`
}

// PromptRequest is the assembled model request with stable cache prefix marks.
type PromptRequest struct {
	UserID   string
	AgentID  string
	RunID    string
	TeamID   string
	Tools    []ToolDef
	System   string
	Messages []Message
	Cache    []Breakpoint
}

// PromptResponse is the model's output for one turn.
type PromptResponse struct {
	Text         string
	ToolCalls    []ToolCall
	StopReason   string
	Model        string
	Usage        kernel.TokenUsage
	CostMicroUSD *int64
	Price        *kernel.TokenPrice
}

// ModelClient executes one turn completion against the gateway.
type ModelClient interface {
	Complete(ctx context.Context, req PromptRequest) (PromptResponse, error)
}

// PolicyDecision is the pre-writeguard connector/policy decision for a mutating call.
type PolicyDecision struct {
	Verdict  string
	Resource string
	Reason   string
}

// WritePolicy evaluates connector/tool permission before writeguard tightening.
type WritePolicy interface {
	Decide(ctx context.Context, userID, toolName string, op ToolOp, args json.RawMessage) (PolicyDecision, error)
}

// AuditSink records governance events to the durable audit log.
type AuditSink interface {
	Record(ctx context.Context, run *RunState, ev TimelineEvent) error
}

// TimelineSink emits structured timeline events for live/stored turn history.
type TimelineSink interface {
	Emit(ctx context.Context, run *RunState, ev TimelineEvent) error
}

// TurnStatus describes how a single turn ended.
type TurnStatus string

const (
	TurnContinue        TurnStatus = "continue"
	TurnDone            TurnStatus = "done"
	TurnWaitingApproval TurnStatus = "waiting_approval"
	TurnBudgetExhausted TurnStatus = "budget_exhausted"
)

// TurnOutcome summarizes what happened in one RunTurn invocation.
type TurnOutcome struct {
	// History replaces the stored history as a whole; it is not an append. When
	// Engine.ContextWindow > 0, RunTurn trims History with PostTurnBudget
	// before tx3, so TurnOutcome.History and TurnCommit.History are the same
	// trimmed history. Callers persist it and feed it back verbatim. With
	// ContextWindow 0 nothing is trimmed. Trimming saves may precede a failed
	// turn; callers can retry with the original history, which may save again.
	History         []Message
	Status          TurnStatus
	AssistantText   string
	Model           string
	CostMicroUSD    int64
	EstimatedCost   bool
	SpentMicroUSD   int64
	Paused          bool
	Exposure        ExposureState
	Hold            *BatchHold
	BlockedEvents   []TimelineEvent
	SimulatedEvents []TimelineEvent
	Appended        []Message
}

// RunState holds mutable state carried across turns in a run.
type RunState struct {
	RunID          string
	ObjectID       string
	ObjectType     string // "task" or "chat"
	UserID         string
	AgentID        string
	Access         AccessScope
	Budgeted       bool
	BudgetMicroUSD int64
	SpentMicroUSD  int64
	Paused         bool
	DryRun         bool
	Exposure       ExposureState
	SeatID         string
	Catalog        *ToolCatalog
	ApprovedCalls  map[string]string
	Seq            int
}

var (
	// ErrInvalidConfig reports missing required dependencies on Engine or RunState.
	ErrInvalidConfig = errors.New("agentloop: invalid config")
	// ErrContextExceeded means the system, tools, and untrimmable messages exceed the model window.
	ErrContextExceeded = errors.New("agentloop: context window exceeded")
	// ErrDuplicateAction reports a call_id already in dialing, done, or simulated state.
	ErrDuplicateAction = errors.New("agentloop: action already dialing or done")
)
