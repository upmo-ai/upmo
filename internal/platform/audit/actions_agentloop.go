package audit

// Agent loop tool governance actions.
const (
	// AgentToolBlocked records a tool call refused by policy or writeguard.
	AgentToolBlocked Action = "agent.tool_blocked"
	// AgentToolHeld records one or more tool calls paused for user approval.
	AgentToolHeld Action = "agent.tool_held"
	// AgentToolSimulated records a mutating tool call shadow-executed in dry_run mode.
	AgentToolSimulated Action = "agent.tool_simulated"
)

func init() {
	register(AgentToolBlocked, "tool", classAPIActivity, activityUnknown, "tool", "reason", "untrusted_origin")
	register(AgentToolHeld, "hold", classAPIActivity, activityUnknown, "tool", "count", "untrusted_origin")
	register(AgentToolSimulated, "tool", classAPIActivity, activityUnknown, "tool", "dry_run", "untrusted_origin")
}
