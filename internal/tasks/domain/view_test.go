package domain

import "testing"

func TestShowCollapsesTheStoredStatus(t *testing.T) {
	tests := []struct {
		status Status
		want   Shown
	}{
		{Preparing, ShownWorking},
		{Planning, ShownWorking},
		{Working, ShownWorking},
		{Checking, ShownWorking},
		{NeedsApproval, ShownNeedsApproval},
		{Done, ShownDone},
		{Failed, ShownFailed},
	}
	for _, test := range tests {
		got, err := Show(test.status)
		if err != nil || got != test.want {
			t.Errorf("Show(%q) = %q, %v; want %q", test.status, got, err, test.want)
		}
	}
	if _, err := Show("Sideways"); err == nil {
		t.Error("Show of an unknown status was accepted")
	}
}

func TestPhaseOf(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		last   Status
		want   Status
	}{
		{"preparing is its own phase", Preparing, Working, Preparing},
		{"planning is its own phase", Planning, "", Planning},
		{"working is its own phase", Working, Checking, Working},
		{"checking is its own phase", Checking, "", Checking},
		{"needs approval keeps the last phase", NeedsApproval, Working, Working},
		{"done keeps the last phase", Done, Checking, Checking},
		{"failed keeps the last phase", Failed, Preparing, Preparing},
		{"no earlier phase is Working", Failed, "", Working},
		{"a last that is not a phase is ignored", Done, Failed, Working},
	}
	for _, test := range tests {
		if got := PhaseOf(test.status, test.last); got != test.want {
			t.Errorf("%s: PhaseOf(%q, %q) = %q, want %q", test.name, test.status, test.last, got, test.want)
		}
	}
}

func TestEventMessage(t *testing.T) {
	tests := []struct {
		kind Kind
		to   Status
		want string
	}{
		{Open, Preparing, "Task opened"},
		{Advance, Working, "Moved on to Working"},
		{Advance, Checking, "Moved on to Checking"},
		{Plan, Planning, "Planning"},
		{Pause, NeedsApproval, "Waiting for approval"},
		{Resume, Working, "Approval given"},
		{Pass, Done, "Checks passed"},
		{Fail, Failed, "Task failed"},
	}
	for _, test := range tests {
		got, ok := EventMessage(test.kind, test.to)
		if !ok || got != test.want {
			t.Errorf("EventMessage(%q, %q) = %q, %t; want %q", test.kind, test.to, got, ok, test.want)
		}
	}
	if got, ok := EventMessage(Chat, Working); ok || got != "" {
		t.Errorf("EventMessage(chat) = %q, %t", got, ok)
	}
}

func TestResultText(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{"no result", nil, ""},
		{"json null", []byte("null"), ""},
		{"a string is used as is", []byte(`"All filed"`), "All filed"},
		{"an object is compact json", []byte(`{ "summary": "All filed",  "detail": "" }`), `{"summary":"All filed","detail":""}`},
		{"a number is compact json", []byte(` 42 `), "42"},
		{"not json", []byte(`{`), ""},
	}
	for _, test := range tests {
		if got := ResultText(test.raw); got != test.want {
			t.Errorf("%s: ResultText(%s) = %q, want %q", test.name, test.raw, got, test.want)
		}
	}
}
