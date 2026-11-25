package audit

// The chat actions. A line's picks are recorded as ids and names only, never
// the line's text.
const (
	// ChatCreate opens one chat.
	ChatCreate Action = "chat.create"
	// ChatMessage appends one line to a chat.
	ChatMessage Action = "chat.message"
	// ChatRetry asks again for the reply to one failed reply's user line.
	ChatRetry Action = "chat.retry"
	// ChatBranch copies a chat up to one line into a new private chat.
	ChatBranch Action = "chat.branch"
	// ChatSweep deletes reply deltas whose line never arrived.
	ChatSweep       Action = "chat.sweep"
	ChatStop        Action = "chat.stop"
	ChatUpdate      Action = "chat.update"
	ChatDelete      Action = "chat.delete"
	ChatHoldApprove Action = "chat.hold_approve"
	ChatHoldDeny    Action = "chat.hold_deny"
	// ChatShare replaces one chat's share.
	ChatShare Action = "chat.share"
	// AgentCreate stores one agent profile.
	AgentCreate Action = "agent.create"
	// AgentUpdate changes one agent profile's fields. The prompt text is
	// content and never appears.
	AgentUpdate Action = "agent.update"
	// AgentShare changes who an agent profile is for.
	AgentShare Action = "agent.share"
	// AgentDelete removes one agent profile.
	AgentDelete Action = "agent.delete"
)

func init() {
	register(ChatCreate, "chat", classAPIActivity, activityUnknown)
	register(ChatMessage, "line", classAPIActivity, activityUnknown, "route", "model_provider", "model_id", "agent_id", "attachment_count")
	register(ChatRetry, "line", classAPIActivity, activityUnknown, "route", "model_provider", "model_id", "agent_id")
	register(ChatBranch, "chat", classAPIActivity, activityUnknown, "source_chat_id", "up_to_line_id", "retry")
	register(ChatSweep, "delta", classAPIActivity, activityUnknown, "rows")
	register(ChatStop, "line", classAPIActivity, activityUnknown, "status")
	register(ChatUpdate, "chat", classAPIActivity, activityUnknown, "title")
	register(ChatDelete, "chat", classAPIActivity, activityUnknown)
	register(ChatHoldApprove, "hold", classAPIActivity, activityUnknown, "state", "allow_for_chat")
	register(ChatHoldDeny, "hold", classAPIActivity, activityUnknown, "state")
	register(ChatShare, "chat", classAPIActivity, activityUnknown, "grants", "visibility")
	register(AgentCreate, "agent", classAPIActivity, activityUnknown, "enabled", "route", "model_provider", "model_id", "visibility", "team_ids")
	register(AgentUpdate, "agent", classAPIActivity, activityUnknown, "enabled", "route", "model_provider", "model_id", "visibility", "team_ids")
	register(AgentShare, "agent", classAPIActivity, activityUnknown, "visibility", "team_ids")
	register(AgentDelete, "agent", classAPIActivity, activityUnknown)
}
