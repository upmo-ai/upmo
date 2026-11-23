package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/writeguard"
)

// AllowanceChecker reports whether the person allowed toolName for chatID.
type AllowanceChecker interface {
	HasAllowance(ctx context.Context, access Access, chatID, toolName string) (bool, error)
}

// CanAllowForChat reports whether "Allow for this chat" can cover a call.
// Delete tools, create_automation and tainted chats never qualify.
func CanAllowForChat(toolName string, op agentloop.ToolOp, tainted bool) bool {
	name := strings.TrimSpace(toolName)
	return !tainted && op == agentloop.OpChange && name != "" && name != "create_automation"
}

// ChatWritePolicy is the chat's write policy: a call runs without a hold only
// when the person allowed its tool for this chat and nothing tainted it. Claim
// preloads the person's allowances into Allowed for the round, so a tool call
// costs no transaction; Allowances is asked only when Allowed is nil.
type ChatWritePolicy struct {
	ChatID     string
	Run        *agentloop.RunState
	Allowed    map[string]bool
	Allowances AllowanceChecker
	Access     func() (Access, error)
}

// Decide answers allow or ask for one call.
func (p ChatWritePolicy) Decide(ctx context.Context, _ string, toolName string, op agentloop.ToolOp, _ json.RawMessage) (agentloop.PolicyDecision, error) {
	ask := agentloop.PolicyDecision{Verdict: string(writeguard.Ask)}
	allow := agentloop.PolicyDecision{Verdict: string(writeguard.Allow)}
	if !CanAllowForChat(toolName, op, p.Run != nil && p.Run.Exposure.Tainted) {
		return ask, nil
	}
	name := strings.TrimSpace(toolName)
	if p.Allowed != nil {
		if p.Allowed[name] {
			return allow, nil
		}
		return ask, nil
	}
	if p.Allowances == nil || p.Access == nil {
		return ask, nil
	}
	access, err := p.Access()
	if err != nil {
		return agentloop.PolicyDecision{}, err
	}
	ok, err := p.Allowances.HasAllowance(ctx, access, p.ChatID, name)
	if err != nil {
		return agentloop.PolicyDecision{}, err
	}
	if !ok {
		return ask, nil
	}
	return allow, nil
}
