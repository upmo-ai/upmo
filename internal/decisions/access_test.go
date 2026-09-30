package decisions

import "testing"

func TestAccessArgsKeepTeamsAndShares(t *testing.T) {
	got := accessArgs(Access{UserID: "u", Allow: "settings:manage", Teams: "{t1}", SharedView: "{v1,e1}", SharedEdit: "{e1}"})
	want := []any{"u", "settings:manage", "{t1}", "{v1,e1}", "{e1}"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v", got)
		}
	}
}

func TestAccessArgsDefaultToEmptyArrays(t *testing.T) {
	got := accessArgs(Access{UserID: "u", Allow: "settings:manage"})
	for i, arg := range got[2:] {
		if arg != "{}" {
			t.Fatalf("arg %d = %v", i+2, arg)
		}
	}
}
