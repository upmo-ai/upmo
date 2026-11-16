package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanLessonAndRedact(t *testing.T) {
	title, cue, body, err := CleanLesson("  VAT check ", " When booking EU invoices ", " Email ada@example.com or Bearer secret1234567890 ")
	if err != nil {
		t.Fatal(err)
	}
	if title != "VAT check" || cue != "When booking EU invoices" {
		t.Fatalf("got title=%q cue=%q", title, cue)
	}
	if strings.Contains(body, "ada@example.com") || strings.Contains(body, "secret1234567890") {
		t.Fatalf("unredacted body = %q", body)
	}
	if _, _, _, err := CleanLesson("", "cue", "body"); !errors.Is(err, ErrLessonTitle) {
		t.Fatalf("empty title = %v", err)
	}
	if _, _, _, err := CleanLesson("title", "", "body"); !errors.Is(err, ErrLessonCue) {
		t.Fatalf("empty cue = %v", err)
	}
	if _, _, _, err := CleanLesson("title", "cue", ""); !errors.Is(err, ErrLessonBody) {
		t.Fatalf("empty body = %v", err)
	}
	if _, _, _, err := CleanLesson(strings.Repeat("a", LessonTitleRunes+1), "cue", "body"); !errors.Is(err, ErrLesson) {
		t.Fatalf("long title = %v", err)
	}
	if _, _, _, err := CleanLesson("title", strings.Repeat("a", LessonCueRunes+1), "body"); !errors.Is(err, ErrLesson) {
		t.Fatalf("long cue = %v", err)
	}
	if _, _, _, err := CleanLesson("title", "cue", strings.Repeat("a", LessonBodyRunes+1)); !errors.Is(err, ErrLesson) {
		t.Fatalf("long body = %v", err)
	}
}

func TestAgentSystemIncludesOnlyInlineLessons(t *testing.T) {
	lessons := []Lesson{
		{Title: "Always", Body: "Check currency.", AlwaysInline: true},
		{Title: "OnDemand", Body: "Rare rule.", AlwaysInline: false},
	}
	overlay := AppendInlineLessons("  Be accurate. ", lessons)
	if overlay != "Be accurate.\n\n## Always\n\nCheck currency." {
		t.Fatalf("AppendInlineLessons = %q", overlay)
	}
	if emptyOverlay := AppendInlineLessons("  ", []Lesson{{Title: "X", Body: "Y", AlwaysInline: false}}); emptyOverlay != "" {
		t.Fatalf("empty AppendInlineLessons = %q", emptyOverlay)
	}
	got := AgentSystem("Bookkeeper", "Be accurate.", lessons)
	if !strings.Contains(got, "# Agent: Bookkeeper") ||
		!strings.Contains(got, "Be accurate.") ||
		!strings.Contains(got, "## Always\n\nCheck currency.") ||
		strings.Contains(got, "Rare rule.") {
		t.Fatalf("AgentSystem = %q", got)
	}
	if empty := AgentSystem("Empty", "", []Lesson{{Title: "X", Body: "Y", AlwaysInline: false}}); empty != "" {
		t.Fatalf("empty AgentSystem = %q", empty)
	}
}
