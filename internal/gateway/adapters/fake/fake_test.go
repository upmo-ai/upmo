package fake

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// stream runs one stream for a model and returns what it sent, how long the
// gaps between words were at most, and how it ended.
func stream(t *testing.T, a Adapter, model string) (words []string, gap time.Duration, class domain.FailureClass, err error) {
	t.Helper()
	last := time.Now()
	_, class, err = a.ChatStream(context.Background(), app.Attempt{Model: model}, domain.Request{}, func(word string) error {
		now := time.Now()
		gap = max(gap, now.Sub(last))
		last = now
		words = append(words, word)
		return nil
	})
	return words, gap, class, err
}

func TestScriptedOKStreamsTheReplyThenEnds(t *testing.T) {
	words, _, class, err := stream(t, Adapter{Reply: "ok"}, ModelOK)
	if class != domain.ClassOK || err != nil {
		t.Fatalf("fake-ok ended %s, %v", class, err)
	}
	got := strings.Join(words, "")
	if !strings.HasPrefix(got, "fake-ok says: ") || len(words) < 5 {
		t.Fatalf("fake-ok said %q in %d words", got, len(words))
	}
}

func TestScriptedSlowPausesBetweenWords(t *testing.T) {
	words, gap, class, err := stream(t, Adapter{}, ModelSlow)
	if class != domain.ClassOK || err != nil {
		t.Fatalf("fake-slow ended %s, %v", class, err)
	}
	if !strings.HasPrefix(strings.Join(words, ""), "fake-slow says: ") {
		t.Fatalf("fake-slow said %q", strings.Join(words, ""))
	}
	if gap < slowDelay-50*time.Millisecond {
		t.Fatalf("fake-slow words came %s apart, want about %s", gap, slowDelay)
	}
}

func TestScriptedRateLimitFailsBeforeTheFirstWord(t *testing.T) {
	words, _, class, err := stream(t, Adapter{}, ModelFailRateLimit)
	if class != domain.ClassRateLimit || err == nil {
		t.Fatalf("fake-fail-ratelimit ended %s, %v", class, err)
	}
	if len(words) != 0 {
		t.Fatalf("fake-fail-ratelimit sent %q before failing", words)
	}
}

func TestScriptedFailAfterTwoStreamsTwoWordsThenFails(t *testing.T) {
	words, _, class, err := stream(t, Adapter{}, ModelFailAfterTwo)
	if class != domain.ClassTransient || err == nil {
		t.Fatalf("fake-fail-after-2 ended %s, %v", class, err)
	}
	if len(words) != 2 || strings.Join(words, "") != "fake-fail-after-2 says: " {
		t.Fatalf("fake-fail-after-2 sent %q", words)
	}
}

func TestAnyOtherModelKeepsTheAdapterFields(t *testing.T) {
	words, _, class, err := stream(t, Adapter{Reply: "plain reply"}, "gpt-x")
	if class != domain.ClassOK || err != nil || strings.Join(words, "") != "plain reply " {
		t.Fatalf("an unscripted model sent %q, ended %s, %v", words, class, err)
	}
	words, _, class, _ = stream(t, Adapter{Reply: "a b c", StreamFailClass: domain.ClassTransient, StreamFailWords: 1}, "")
	if class != domain.ClassTransient || len(words) != 1 {
		t.Fatalf("the failure fields did not apply: %q, %s", words, class)
	}
}

func TestFakeToolCallsAndErrorToolResult(t *testing.T) {
	a := Adapter{}
	turn0, err := domain.Build(domain.Input{
		Tools:    []domain.Tool{{Name: "search"}},
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, class, err := a.Chat(context.Background(), app.Attempt{Model: ModelToolCall}, turn0)
	if err != nil || class != domain.ClassOK || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "search" {
		t.Fatalf("turn0 chat = %+v class=%s err=%v", res, class, err)
	}

	var deltas []app.ToolCallDelta
	sres, class, err := a.ChatStreamTools(context.Background(), app.Attempt{Model: ModelToolCall}, turn0, app.StreamEvents{
		ToolCall: func(d app.ToolCallDelta) error {
			deltas = append(deltas, d)
			return nil
		},
	})
	if err != nil || class != domain.ClassOK || len(sres.ToolCalls) != 1 || len(deltas) != 2 {
		t.Fatalf("turn0 stream = %+v deltas=%+v class=%s err=%v", sres, deltas, class, err)
	}

	turn1 := domain.Request{
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "hi"},
			{Role: domain.RoleAssistant, ToolCalls: res.ToolCalls},
			{Role: domain.RoleTool, ToolID: res.ToolCalls[0].ID, ToolName: "search", Text: "not found", IsError: true},
		},
	}
	res1, class, err := a.Chat(context.Background(), app.Attempt{Model: ModelToolCall}, turn1)
	if err != nil || class != domain.ClassOK || res1.Text != "tool_error: not found" {
		t.Fatalf("turn1 chat = %+v class=%s err=%v", res1, class, err)
	}

	configured := Adapter{
		ToolCalls: []domain.ToolCall{{ID: "c1", Name: "ping", Arguments: json.RawMessage(`{"x":1}`)}},
		Usage:     domain.Usage{Arrived: true, Input: 5, Output: 3, Reasoning: 2},
	}
	cres, class, err := configured.Chat(context.Background(), app.Attempt{Model: "custom"}, turn0)
	if err != nil || class != domain.ClassOK || len(cres.ToolCalls) != 1 || cres.Usage.Reasoning != 2 {
		t.Fatalf("configured chat = %+v class=%s err=%v", cres, class, err)
	}
}
