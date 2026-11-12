//go:build integration

package chat_test

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

const adminUser = "018f3b2c-7a10-7c4a-8e11-999999999999"

var adminAccess = chat.Access{UserID: adminUser, Allow: "chat:create,chat:view,chat:edit,agent:create,agent:view,agent:edit,agent:delete,agent:share,settings:manage"}

// answer makes the stub gateway answer every call.
func (r *rig) answer() {
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
}

// sayWith writes a line with picks as the owner and runs its reply.
func (r *rig) sayWith(chatID string, picks app.Picks) {
	r.t.Helper()
	if err := app.Say(context.Background(), r.store, ownerAccess, chatID, "q", picks); err != nil {
		r.t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) agent(access chat.Access, fields app.AgentFields) app.Agent {
	r.t.Helper()
	made, err := r.store.CreateAgent(context.Background(), access, fields)
	if err != nil {
		r.t.Fatal(err)
	}
	return made
}

func TestTheAgentsInstructionsRouteAndModelReachTheGateway(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.answer()
	chatID := r.open()
	agent := r.agent(ownerAccess, app.AgentFields{
		Name: "Research", PromptOverlay: "Answer as a researcher.", Route: "deep",
		ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true,
	})

	r.sayWith(chatID, app.Picks{AgentID: agent.ID})

	calls := r.stub.seen()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	call := calls[0]
	if want := "# Agent: Research\n\nAnswer as a researcher."; call.System != want {
		t.Fatalf("system = %q, want %q", call.System, want)
	}
	if call.Route != "deep" {
		t.Fatalf("route = %q, want the agent's route when the composer gave none", call.Route)
	}
	if call.Pin == nil || call.Pin.Provider != "OpenAI" || call.Pin.Model != "gpt-x" || call.Pin.AccountID != "" {
		t.Fatalf("pin = %+v, want the agent's model by provider name", call.Pin)
	}
	if r.lines(chatID)[1].Status != domain.StatusDone {
		t.Fatalf("lines = %+v", r.lines(chatID))
	}
}

func TestTheComposersRouteAndModelWinOverTheAgents(t *testing.T) {
	const account = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa01"
	r := newRig(t, app.ReplyConfig{})
	r.answer()
	chatID := r.open()
	agent := r.agent(ownerAccess, app.AgentFields{
		Name: "Research", PromptOverlay: "Answer as a researcher.", Route: "deep",
		ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true,
	})

	r.sayWith(chatID, app.Picks{AgentID: agent.ID, Route: "fast", PinAccount: account, PinModel: "gpt-y"})

	call := r.stub.seen()[0]
	if call.Route != "fast" {
		t.Fatalf("route = %q, want the composer's", call.Route)
	}
	if call.Pin == nil || call.Pin.AccountID != account || call.Pin.Model != "gpt-y" || call.Pin.Provider != "" {
		t.Fatalf("pin = %+v, want the composer's pin alone", call.Pin)
	}
	if call.System != "# Agent: Research\n\nAnswer as a researcher." {
		t.Fatalf("system = %q: the agent's instructions still apply", call.System)
	}
}

func TestAModelOnlyAgentUsesTheInstallDefaultRoute(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.answer()
	chatID := r.open()
	agent := r.agent(ownerAccess, app.AgentFields{Name: "Fast", ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true	})

	r.sayWith(chatID, app.Picks{AgentID: agent.ID})

	call := r.stub.seen()[0]
	if call.Route != "chat" || call.System != "" || call.Pin == nil || call.Pin.Provider != "OpenAI" {
		t.Fatalf("call = %+v, want the install route, no system text and the agent's model", call)
	}
}

func TestAGlobalAgentWorksForAMember(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.answer()
	chatID := r.open()
	house := r.agent(adminAccess, app.AgentFields{Name: "House", PromptOverlay: "House rules.", Enabled: true, Visibility: "everyone"})

	r.sayWith(chatID, app.Picks{AgentID: house.ID})

	if call := r.stub.seen()[0]; call.System != "# Agent: House\n\nHouse rules." {
		t.Fatalf("system = %q", call.System)
	}
}

func TestAReplyWithAnAgentThatIsNotAvailableFailsPlainlyAndAsksNoModel(t *testing.T) {
	const missing = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa03"
	r := newRig(t, app.ReplyConfig{})
	r.answer()
	chatID := r.open()
	disabled := r.agent(ownerAccess, app.AgentFields{Name: "Off", Enabled: false})
	foreign := r.agent(otherAccess, app.AgentFields{Name: "Theirs", Enabled: true})
	offGlobal := r.agent(adminAccess, app.AgentFields{Name: "Off house", Enabled: false, Visibility: "everyone"})
	var team string
	if err := r.owner.QueryRow(context.Background(),
		`INSERT INTO identity.teams (name) VALUES ('Finance') RETURNING id::text`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	teamAgent := r.agent(adminAccess, app.AgentFields{
		Name: "Finance", Enabled: true, Visibility: domain.VisibilityTeam, TeamIDs: []string{team},
	})
	// The author manages agents, so the team agent is one they can see — but
	// they are not on its team, so a reply must not run it.
	r.setAccess(ownerUser, chat.Access{UserID: ownerUser, Allow: ownerAccess.Allow + ",settings:manage"})

	for name, id := range map[string]string{
		"missing": missing, "disabled": disabled.ID, "foreign": foreign.ID,
		"disabled global": offGlobal.ID, "team off the roster": teamAgent.ID,
	} {
		before := len(r.lines(chatID))
		r.sayWith(chatID, app.Picks{AgentID: id})
		lines := r.lines(chatID)
		if len(lines) != before+2 {
			t.Fatalf("%s: lines = %+v", name, lines)
		}
		reply := lines[len(lines)-1]
		if reply.Role != domain.RoleAssistant || reply.Status != domain.StatusFailed || reply.Body != "" ||
			len(reply.Attempts) != 1 || reply.Attempts[0].Class != domain.ClassAgent {
			t.Fatalf("%s: reply = %+v, want a failed reply for the agent", name, reply)
		}
		if got := domain.Reason(reply.Attempts[0].Class); got != "that agent is not available" {
			t.Fatalf("%s: reason = %q", name, got)
		}
		r.completed(r.lastJob())
	}
	if got := r.stub.seen(); len(got) != 0 {
		t.Fatalf("the gateway was asked %d times, want never", len(got))
	}
	if left := r.deltas(chatID); len(left) != 0 {
		t.Fatalf("deltas left behind = %+v", left)
	}
}
