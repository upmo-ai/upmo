package agentloop

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

const trimPreviewBytes = 120

// PostTurnThreshold returns the message budget after reserving 20% headroom,
// the system and tool tokens, and the caller's safety margin.
func PostTurnThreshold(contextWindow, systemTokens, toolTokens, margin int) int {
	return contextWindow*80/100 - systemTokens - toolTokens - margin
}

// PromptMessageBudget returns the maximum message-token estimate that fits in
// contextWindow after accounting for system text, tool definitions, and a 5%
// safety margin. A negative result means every eligible tool result must be trimmed.
func PromptMessageBudget(contextWindow int, system string, tools []ToolDef) int {
	systemTokens := TokenEstimate(system)
	return contextWindow - systemTokens - toolTokens(tools) - contextWindow/20
}

// PostTurnBudget returns the history budget for persistence after reserving
// 20% headroom, system and tool tokens, and a 5% safety margin.
func PostTurnBudget(contextWindow int, system string, tools []ToolDef) int {
	systemTokens := TokenEstimate(system)
	return PostTurnThreshold(contextWindow, systemTokens, toolTokens(tools), contextWindow/20)
}

func toolTokens(tools []ToolDef) int {
	total := 0
	for _, tool := range tools {
		total += TokenEstimate(tool.Name) + TokenEstimate(tool.Description) + TokenEstimate(string(tool.Schema))
	}
	return total
}

// TrimMessages replaces the oldest eligible tool results until the estimated
// message tokens are at or below threshold. A zero window is a no-op.
func TrimMessages(ctx context.Context, store OutputStore, run *RunState, messages []Message, contextWindow, threshold int) ([]Message, error) {
	if contextWindow <= 0 || estimateMessages(messages) <= threshold {
		return cloneMessages(messages), nil
	}
	trimmed := cloneMessages(messages)
	latestCalls := make(map[string]bool)
	for i := len(trimmed) - 1; i >= 0; i-- {
		if trimmed[i].Role == RoleAssistant {
			for _, call := range trimmed[i].ToolCalls {
				latestCalls[call.ID] = true
			}
			break
		}
	}
	for i := range trimmed {
		message := &trimmed[i]
		if estimateMessages(trimmed) <= threshold {
			break
		}
		if message.Role != RoleTool || message.Text == "" || message.HeldPlaceholder || latestCalls[message.ToolID] || message.Offload {
			continue
		}
		original := message.Text
		preview := ""
		reference := "[tool result trimmed; output store unavailable]"
		if store != nil {
			preview = strings.TrimSpace(original)
			if len(preview) > trimPreviewBytes {
				preview = preview[:trimPreviewBytes]
				for !utf8.ValidString(preview) {
					preview = preview[:len(preview)-1]
				}
				preview += "…"
			}
			reference = fmt.Sprintf("%s:%s\n%s", toolRetrieveOutput, strings.Repeat("x", 36), preview)
		}
		if len(reference) >= len(original) {
			continue
		}
		if store != nil {
			id, err := store.SaveOutput(ctx, run, message.ToolName, original, run != nil && run.Exposure.Tainted)
			if err != nil {
				return nil, err
			}
			reference = fmt.Sprintf("%s:%s\n%s", toolRetrieveOutput, id, preview)
		}
		message.Text = reference
		message.Offload = store != nil
	}
	return trimmed, nil
}

func estimateMessages(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += TokenEstimate(message.Text) + TokenEstimate(message.ToolName) + TokenEstimate(message.ToolID)
		for _, call := range message.ToolCalls {
			total += TokenEstimate(call.Name) + TokenEstimate(call.ID) + TokenEstimate(string(call.Arguments))
		}
	}
	return total
}

func cloneMessages(messages []Message) []Message {
	cloned := make([]Message, len(messages))
	copy(cloned, messages)
	for i := range cloned {
		cloned[i].ToolCalls = append([]ToolCall(nil), messages[i].ToolCalls...)
	}
	return cloned
}
