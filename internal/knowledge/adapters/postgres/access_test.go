package postgres

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/knowledge/app"
)

func TestAccessArgsKeepTeamsAndShares(t *testing.T) {
	got := accessArgs(app.Access{UserID: "u", Allow: "folder:view", Teams: "{t1}", SharedView: "{v1,e1}", SharedEdit: "{e1}"})
	want := []any{"u", "folder:view", "{t1}", "{v1,e1}", "{e1}"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v", got)
		}
	}
}

func TestAccessArgsDefaultToEmptyArrays(t *testing.T) {
	got := accessArgs(app.Access{UserID: "u", Allow: "folder:view"})
	for i, arg := range got[2:] {
		if arg != "{}" {
			t.Fatalf("arg %d = %v", i+2, arg)
		}
	}
}
