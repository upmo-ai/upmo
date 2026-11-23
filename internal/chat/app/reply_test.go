package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

func TestHistoryKeepsTheNewestLinesThatFitAndAlwaysTheLast(t *testing.T) {
	lines := []Line{
		{Role: "user", Body: "aaaaaaaa"},
		{Role: "assistant", Body: "bbbb"},
		{Role: "user", Body: "cc"},
	}
	got := history(lines, 6)
	want := []ModelMessage{{Role: "assistant", Text: "bbbb"}, {Role: "user", Text: "cc"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %+v", got)
	}
	// The last line is sent even when it alone is over the budget.
	if got := history(lines, 1); !reflect.DeepEqual(got, []ModelMessage{{Role: "user", Text: "cc"}}) {
		t.Fatalf("tiny budget history = %+v", got)
	}
	// The budget counts characters, not bytes.
	if got := history([]Line{{Role: "user", Body: "ééé"}, {Role: "user", Body: "ü"}}, 4); len(got) != 1 || got[0].Text != "ééé\n\nü" {
		t.Fatalf("multibyte history = %+v", got)
	}
}

func TestHistoryJoinsNeighboringLinesOfOneRole(t *testing.T) {
	got := history([]Line{
		{Role: "user", Body: "q1"},
		{Role: "user", Body: "q2"},
		{Role: "assistant", Body: "a"},
	}, 100)
	want := []ModelMessage{{Role: "user", Text: "q1\n\nq2"}, {Role: "assistant", Text: "a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %+v", got)
	}
}

func TestCloseAttemptsDoesNotListTheLastFailureTwice(t *testing.T) {
	notices := []domain.Attempt{{Model: "m1", Class: "rate_limit"}, {Model: "m2", Class: "transient"}}
	got := closeAttempts(notices, &ModelError{Class: "transient", Model: "m2"})
	if !reflect.DeepEqual(got, notices) {
		t.Fatalf("attempts = %+v", got)
	}
	got = closeAttempts(notices[:1], &ModelError{Class: "transient", Model: "m2"})
	if !reflect.DeepEqual(got, notices) {
		t.Fatalf("attempts = %+v", got)
	}
}

func TestTruncateCutsOnRunes(t *testing.T) {
	if got := truncate("ééé", 2); got != "éé" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("ab", 2); got != "ab" {
		t.Fatalf("truncate = %q", got)
	}
}

func TestAReplyAsksWithTheRouteTheModelAndTheSystemTextTheAgentLeavesOpen(t *testing.T) {
	w := &replyWorker{cfg: ReplyConfig{}.withDefaults()}
	agent := &Agent{Name: "Research", PromptOverlay: "Be brief.", Route: "deep", ModelProvider: "OpenAI", ModelID: "gpt-x"}
	system := "# Agent: Research\n\nBe brief."

	// The composer picked no route: the line carries the install's default.
	route, got := w.route(ReplyJobArgs{UserID: "u", Route: "chat", AgentID: "a"}, agent)
	if want := (TurnRoute{Route: "deep", PinProvider: "OpenAI", PinModel: "gpt-x", MaxOutputTokens: DefaultMaxOutputTokens}); !reflect.DeepEqual(route, want) {
		t.Fatalf("route = %+v, want %+v", route, want)
	}
	if got != system {
		t.Fatalf("system = %q, want %q", got, system)
	}

	// The composer picked a route and a model: both win, the text stays.
	route, got = w.route(ReplyJobArgs{UserID: "u", Route: "fast", RoutePicked: true, PinAccount: "acct", PinModel: "gpt-y", AgentID: "a"}, agent)
	if want := (TurnRoute{Route: "fast", PinAccount: "acct", PinModel: "gpt-y", MaxOutputTokens: DefaultMaxOutputTokens}); !reflect.DeepEqual(route, want) {
		t.Fatalf("route = %+v, want %+v", route, want)
	}
	if got != system {
		t.Fatalf("system = %q, want %q", got, system)
	}

	// No agent: the line's own route, no system text, no pin.
	route, got = w.route(ReplyJobArgs{UserID: "u", Route: "chat"}, nil)
	if want := (TurnRoute{Route: "chat", MaxOutputTokens: DefaultMaxOutputTokens}); !reflect.DeepEqual(route, want) {
		t.Fatalf("route = %+v, want %+v", route, want)
	}
	if got != "" {
		t.Fatalf("system = %q, want empty", got)
	}
}

func TestTheReplyWorkerRefusesToBeBuiltWithoutAnAgentLookup(t *testing.T) {
	if _, err := NewReplyWorker(ReplyDeps{}); !errors.Is(err, ErrNoAgents) {
		t.Fatalf("NewReplyWorker without Agents = %v, want ErrNoAgents", err)
	}
	if err := AddReplyWorker(river.NewWorkers(), ReplyDeps{}); !errors.Is(err, ErrNoAgents) {
		t.Fatalf("AddReplyWorker without Agents = %v, want ErrNoAgents", err)
	}
}
