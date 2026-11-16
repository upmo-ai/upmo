package domain

import (
	"encoding/json"
	"testing"
)

func TestCanonicalSnapshotDeterminism(t *testing.T) {
	s1 := Snapshot{
		Name:          "Bookkeeper",
		Slug:          "bookkeeper",
		Instructions:  "Verify totals.",
		PlanApproval:  PlanApprovalRisky,
		CheckCriteria: []json.RawMessage{json.RawMessage(`{"b":1,"a":2}`)},
		Enabled:       true,
		Visibility:    VisibilityPrivate,
		InlineLessons: []SnapshotLesson{
			{Title: "B", Cue: "c2", Body: "b2", AlwaysInline: true},
			{Title: "A", Cue: "c1", Body: "b1", AlwaysInline: true},
		},
	}
	s2 := Snapshot{
		Name:          "Bookkeeper",
		Slug:          "bookkeeper",
		Instructions:  "Verify totals.",
		PlanApproval:  PlanApprovalRisky,
		CheckCriteria: []json.RawMessage{json.RawMessage(`{ "a" : 2, "b" : 1 }`)},
		Enabled:       true,
		Visibility:    VisibilityPrivate,
		InlineLessons: []SnapshotLesson{
			{Title: "A", Cue: "c1", Body: "b1", AlwaysInline: true},
			{Title: "B", Cue: "c2", Body: "b2", AlwaysInline: true},
		},
	}
	raw1, dig1, err := CanonicalSnapshot(s1)
	if err != nil {
		t.Fatal(err)
	}
	raw2, dig2, err := CanonicalSnapshot(s2)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw1) != string(raw2) || dig1 != dig2 || len(dig1) != 64 {
		t.Fatalf("mismatch:\nraw1=%s\nraw2=%s\ndig1=%s\ndig2=%s", raw1, raw2, dig1, dig2)
	}
}
