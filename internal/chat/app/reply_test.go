package app

import (
	"reflect"
	"testing"

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

func TestHistoryJoinsNeighbouringLinesOfOneRole(t *testing.T) {
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
	w := NewReplyWorker(ReplyDeps{}).(*replyWorker)
	agent := &Agent{Name: "Research", PromptOverlay: "Be brief.", Route: "deep", ModelProvider: "OpenAI", ModelID: "gpt-x"}

	// The composer picked no route: the line carries the install's default.
	got := w.request(ReplyJobArgs{UserID: "u", Route: "chat", AgentID: "a"}, agent)
	want := ModelRequest{
		Route: "deep", UserID: "u", System: "# Agent: Research\n\nBe brief.", MaxOutputTokens: DefaultMaxOutputTokens,
		PinProvider: "OpenAI", PinModel: "gpt-x", AgentID: "a",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %+v, want %+v", got, want)
	}

	// The composer picked a route and a model: both win, the text stays.
	got = w.request(ReplyJobArgs{UserID: "u", Route: "fast", RoutePicked: true, PinAccount: "acct", PinModel: "gpt-y", AgentID: "a"}, agent)
	want = ModelRequest{
		Route: "fast", UserID: "u", System: "# Agent: Research\n\nBe brief.", MaxOutputTokens: DefaultMaxOutputTokens,
		PinAccount: "acct", PinModel: "gpt-y", AgentID: "a",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %+v, want %+v", got, want)
	}

	// No agent: the line's own route, no system text, no pin.
	got = w.request(ReplyJobArgs{UserID: "u", Route: "chat"}, nil)
	if want := (ModelRequest{Route: "chat", UserID: "u", MaxOutputTokens: DefaultMaxOutputTokens}); !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
}
