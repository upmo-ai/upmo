package app

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

type fakeAllowances bool

func (f fakeAllowances) HasAllowance(context.Context, Access, string, string) (bool, error) {
	return bool(f), nil
}

func TestChatWritePolicyAndCanAllowForChat(t *testing.T) {
	ops := map[string]agentloop.ToolOp{"read": agentloop.OpRead, "change": agentloop.OpChange, "delete": agentloop.OpDelete}
	for opName, op := range ops {
		for _, tool := range []string{"change_tool", "create_automation", " "} {
			for _, tainted := range []bool{false, true} {
				for _, private := range []bool{false, true} {
					for _, allowed := range []bool{false, true} {
						run := &agentloop.RunState{Exposure: agentloop.ExposureState{Tainted: tainted, PrivateRead: private}}
						p := ChatWritePolicy{Run: run, Allowances: fakeAllowances(allowed),
							Access: func() (Access, error) { return Access{}, nil }}
						got, err := p.Decide(context.Background(), "u", tool, op, nil)
						if err != nil {
							t.Fatal(err)
						}
						want := "ask"
						if allowed && op == agentloop.OpChange && tool == "change_tool" && !tainted {
							want = "allow"
						}
						if got.Verdict != want {
							t.Errorf("op=%s tool=%q tainted=%v private=%v allowed=%v: %s, want %s", opName, tool, tainted, private, allowed, got.Verdict, want)
						}
					}
				}
			}
		}
	}
	if CanAllowForChat("change_tool", agentloop.OpChange, true) || CanAllowForChat("change_tool", agentloop.OpDelete, false) {
		t.Fatal("tainted chats and delete tools never qualify")
	}
}
