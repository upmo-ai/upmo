package domain

import "testing"

func TestEndTurn(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantBody  string
		wantEnded bool
	}{
		{name: "sentinel only", text: "upmo:end", wantEnded: true},
		{name: "text before sentinel", text: "hello\nupmo:end", wantBody: "hello", wantEnded: true},
		{name: "trailing newline", text: "hello\nupmo:end\n", wantBody: "hello", wantEnded: true},
		{name: "sentinel before more text", text: "upmo:end\nmore", wantBody: "upmo:end\nmore"},
		{name: "pass claim", text: "I pass", wantBody: "I pass"},
		{name: "done claim", text: "done", wantBody: "done"},
		{name: "passed claim", text: "passed", wantBody: "passed"},
		{name: "uppercase sentinel", text: "UPMO:END", wantBody: "UPMO:END"},
		{name: "sentinel suffix", text: "upmo:end please", wantBody: "upmo:end please"},
		{name: "indented sentinel", text: "hello\n  upmo:end  ", wantBody: "hello", wantEnded: true},
		{name: "two sentinel lines", text: "upmo:end\nupmo:end", wantBody: "upmo:end", wantEnded: true},
		{name: "empty", text: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, ended := EndTurn(test.text)
			if body != test.wantBody {
				t.Errorf("EndTurn() body = %q, want %q", body, test.wantBody)
			}
			if ended != test.wantEnded {
				t.Errorf("EndTurn() ended = %t, want %t", ended, test.wantEnded)
			}
		})
	}
}
