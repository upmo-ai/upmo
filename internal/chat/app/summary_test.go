package app

import (
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

func TestCutForSummaryAndFormatSummaryPrompt(t *testing.T) {
	lines := []Line{
		{ID: "1", Seq: 1, Role: "user", Body: strings.Repeat("a", 400)},
		{ID: "2", Seq: 2, Role: "assistant", Body: strings.Repeat("b", 400)},
		{ID: "3", Seq: 3, Role: "user", Body: strings.Repeat("c", 400)},
		{ID: "4", Seq: 4, Role: "assistant", Body: strings.Repeat("d", 400)},
		{ID: "5", Seq: 5, Role: "user", Body: "now"},
	}
	if cut, _, _, _, needed := CutForSummary(lines, strings.Repeat("s", 796), 1000); needed || cut != nil {
		t.Fatalf("exact threshold compacted: cut=%v needed=%v", cut, needed)
	}
	cut, keep, id, seq, needed := CutForSummary(lines, strings.Repeat("s", 800), 1000)
	if !needed || len(cut) != 2 || len(keep) != 3 || id != "2" || seq != 2 || keep[0].Seq != 3 || keep[2].Seq != 5 {
		t.Fatalf("cut = %v, keep = %v, cursor = %q/%d, needed = %v", cut, keep, id, seq, needed)
	}
	if got := FormatSummaryPrompt(" summary ", false); got != "summary" {
		t.Fatalf("summary = %q", got)
	}
	if got := FormatSummaryPrompt("summary", true); !strings.Contains(got, `<untrust source="rolling_summary">summary</untrust>`) {
		t.Fatalf("tainted summary = %q", got)
	}
	if got := domain.Reason("context_exceeded"); got != "this chat is too long for this model" {
		t.Fatalf("context reason = %q", got)
	}
}

func TestCutForSummaryRequiresTwoPriorExchangesAndCountsLines(t *testing.T) {
	lines := []Line{{Role: "user", Body: strings.Repeat("x", 3000)}, {Role: "assistant", Body: "a"}, {Role: "user", Body: "now"}}
	if _, _, _, _, needed := CutForSummary(lines, "", 10); needed {
		t.Fatal("compacted with only one prior exchange")
	}
	lines = append(lines, Line{Role: "assistant", Body: "a"}, Line{Role: "user", Body: "now"})
	if _, _, _, _, needed := CutForSummary(lines, "", 10); !needed {
		t.Fatal("did not compact two prior exchanges")
	}
}

func TestFallbackDropExchangesFailsAfterBoundedDrops(t *testing.T) {
	const window = 100
	lines := make([]Line, 0, 25)
	for i := 0; i < 12; i++ {
		lines = append(lines, Line{Role: "user", Body: strings.Repeat("x", 60)}, Line{Role: "assistant", Body: strings.Repeat("y", 60)})
	}
	lines = append(lines, Line{Role: "user", Body: strings.Repeat("z", 60)})
	budget := agentloop.PromptMessageBudget(window, "", nil)
	if estimateAgentMessages(summaryMessages("", false, lines[16:])) <= budget || estimateAgentMessages(summaryMessages("", false, lines[20:])) > budget {
		t.Fatal("prompt must fit only after dropping more than eight exchanges")
	}
	_, err := FallbackDropExchanges(lines, "", false, window, "", nil)
	modelErr, ok := err.(*ModelError)
	if !ok || modelErr.Class != "context_exceeded" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCutForSummaryCountsAttachmentTokensAndIncludesAttachmentsInTranscript(t *testing.T) {
	extracted := `<untrust source="attachment">` + strings.Repeat("budget ", 400) + `</untrust>`
	lines := []Line{
		{ID: "1", Seq: 1, Role: "user", Body: "see attached", Attachments: []domain.Attachment{
			{ID: "f1", Name: "{{notes}}.md", Mime: "text/markdown", SizeBytes: 100, SHA256: "abc", Extracted: extracted},
		}},
		{ID: "2", Seq: 2, Role: "assistant", Body: "ok"},
		{ID: "3", Seq: 3, Role: "user", Body: "second"},
		{ID: "4", Seq: 4, Role: "assistant", Body: "ok"},
		{ID: "5", Seq: 5, Role: "user", Body: "now"},
	}

	// Body alone is tiny (< 10% of window=1000), so only attachment text crosses the 60% threshold.
	if got := LineTokens(lines[0]); got <= agentloop.TokenEstimate(lines[0].Body) {
		t.Fatalf("LineTokens = %d, did not count attachment text", got)
	}
	prompt := LinePromptText(lines[0])
	if !strings.Contains(prompt, "Attachment ({ {notes}}.md):\n"+extracted) {
		t.Fatalf("LinePromptText = %q", prompt)
	}
	cut, keep, id, seq, needed := CutForSummary(lines, "", 1000)
	if !needed || len(cut) != 2 || len(keep) != 3 || id != "2" || seq != 2 {
		t.Fatalf("CutForSummary = cut %d keep %d id %q seq %d needed %v", len(cut), len(keep), id, seq, needed)
	}
	transcript := BuildSummarizerTranscript("", cut)
	if !strings.Contains(transcript, "Attachment ({ {notes}}.md):") || !strings.Contains(transcript, "budget budget") {
		t.Fatalf("BuildSummarizerTranscript omitted attachment text: %q", transcript)
	}
}
