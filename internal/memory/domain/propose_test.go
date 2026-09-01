package domain

import "testing"

func TestParseProposals(t *testing.T) {
	got, err := ParseProposals([]byte(`[{"claim":"Ada prefers tea","changes_behavior":true},{"claim":"Lee lives in Leeds","changes_behavior":false}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Claim != "Ada prefers tea" || !got[0].ChangesBehavior || got[1].ChangesBehavior {
		t.Fatalf("parsed %+v", got)
	}

	if _, err := ParseProposals([]byte(`[]`)); err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{
		`[{"claim":"Ada prefers tea","changes_behavior":true,"extra":1}]`,
		`[{"claim":"Ada prefers tea"}]`,
		`[{"claim":"Ada prefers tea","changes_behavior":true},{"claim":"b","changes_behavior":true},{"claim":"c","changes_behavior":true},{"claim":"d","changes_behavior":true},{"claim":"e","changes_behavior":true},{"claim":"f","changes_behavior":true}]`,
		`[{"claim":"   ","changes_behavior":true}]`,
		`[{"claim":"Ada prefers tea","changes_behavior":true}] trailing`,
		`[{"claim":"Ada prefers tea","changes_behavior":"yes"}]`,
	} {
		if _, err := ParseProposals([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
