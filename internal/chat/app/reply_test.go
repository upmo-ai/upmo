package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

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
