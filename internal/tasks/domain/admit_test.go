package domain

import "testing"

func TestRoom(t *testing.T) {
	tests := []struct {
		name   string
		active int
		want   bool
	}{
		{name: "negative", active: -1, want: false},
		{name: "zero", active: 0, want: true},
		{name: "one", active: 1, want: true},
		{name: "at cap", active: 2, want: false},
		{name: "above cap", active: 3, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Room(test.active); got != test.want {
				t.Fatalf("Room(%d) = %t, want %t", test.active, got, test.want)
			}
		})
	}
}

func TestRunStep(t *testing.T) {
	unknownKind := RunKind("other")
	unknownState := RunState("other")

	tests := []struct {
		name      string
		state     RunState
		input     RunInput
		wantState RunState
		wantErr   error
	}{
		{
			name:      "new admit with room",
			input:     RunInput{Kind: RunAdmit, Room: true},
			wantState: RunAdmitted,
		},
		{
			name:      "new admit without room",
			input:     RunInput{Kind: RunAdmit, Room: false},
			wantState: RunQueued,
		},
		{
			name:      "queued admit with room",
			state:     RunQueued,
			input:     RunInput{Kind: RunAdmit, Room: true},
			wantState: RunAdmitted,
		},
		{
			name:      "queued admit without room",
			state:     RunQueued,
			input:     RunInput{Kind: RunAdmit, Room: false},
			wantState: RunQueued,
		},
		{
			name:      "admitted admit with room",
			state:     RunAdmitted,
			input:     RunInput{Kind: RunAdmit, Room: true},
			wantState: RunAdmitted,
		},
		{
			name:      "admitted admit without room",
			state:     RunAdmitted,
			input:     RunInput{Kind: RunAdmit, Room: false},
			wantState: RunAdmitted,
		},
		{
			name:      "admitted end with room",
			state:     RunAdmitted,
			input:     RunInput{Kind: RunEnd, Room: true},
			wantState: RunEnded,
		},
		{
			name:      "admitted end without room",
			state:     RunAdmitted,
			input:     RunInput{Kind: RunEnd, Room: false},
			wantState: RunEnded,
		},
		{
			name:      "queued end",
			state:     RunQueued,
			input:     RunInput{Kind: RunEnd},
			wantState: RunEnded,
		},
		{
			name:      "ended admit",
			state:     RunEnded,
			input:     RunInput{Kind: RunAdmit},
			wantState: RunEnded,
			wantErr:   ErrTerminal,
		},
		{
			name:      "ended end",
			state:     RunEnded,
			input:     RunInput{Kind: RunEnd},
			wantState: RunEnded,
			wantErr:   ErrTerminal,
		},
		{
			name:      "ended unknown kind",
			state:     RunEnded,
			input:     RunInput{Kind: unknownKind},
			wantState: RunEnded,
			wantErr:   ErrTerminal,
		},
		{
			name:    "new end",
			input:   RunInput{Kind: RunEnd},
			wantErr: ErrInput,
		},
		{
			name:      "unknown kind",
			state:     RunQueued,
			input:     RunInput{Kind: unknownKind},
			wantState: RunQueued,
			wantErr:   ErrInput,
		},
		{
			name:      "unknown state",
			state:     unknownState,
			input:     RunInput{Kind: RunAdmit, Room: true},
			wantState: unknownState,
			wantErr:   ErrInput,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotState, gotErr := RunStep(test.state, test.input)
			if gotState != test.wantState {
				t.Errorf("RunStep() state = %q, want %q", gotState, test.wantState)
			}
			if gotErr != test.wantErr {
				t.Errorf("RunStep() error = %v, want %v", gotErr, test.wantErr)
			}
		})
	}
}
