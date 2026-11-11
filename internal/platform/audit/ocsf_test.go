package audit

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestOCSFGolden(t *testing.T) {
	prev, err := hex.DecodeString(strings.Repeat("00", 32))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hex.DecodeString("0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0")
	if err != nil {
		t.Fatal(err)
	}
	sealed := Sealed{
		Row:      goldenRow(),
		Position: 1,
		PrevHash: prev,
		Hash:     hash,
	}
	got, err := OCSF(sealed)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/ocsf.golden") //nolint:gosec // a fixed path in this package
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("ocsf bytes:\n got %s\nwant %s", got, want)
	}
}

func TestOCSFMapsRefusals(t *testing.T) {
	row := goldenRow()
	row.Outcome = Denied
	row.Reason = "cross-origin"
	got, err := OCSF(Sealed{Row: row, Position: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"status_id":2`, `"status_detail":"denied: cross-origin"`} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}

func TestOCSFRefusesUnknownAction(t *testing.T) {
	row := goldenRow()
	row.Action = "auth.sign_in"
	if _, err := OCSF(Sealed{Row: row}); err == nil {
		t.Fatal("an action outside the catalog mapped without complaint")
	}
}
