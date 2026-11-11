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
	// ChatSweep deletes reply deltas whose line never arrived.
	ChatSweep Action = "chat.sweep"
)

func init() {
	register(ChatCreate, "chat", classAPIActivity, activityUnknown)
	register(ChatMessage, "line", classAPIActivity, activityUnknown, "route", "model_provider", "model_id", "agent_id")
	register(ChatRetry, "line", classAPIActivity, activityUnknown, "route", "model_provider", "model_id", "agent_id")
	register(ChatSweep, "delta", classAPIActivity, activityUnknown, "rows")
}
