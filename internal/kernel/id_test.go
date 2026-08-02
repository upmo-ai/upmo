package kernel

import "testing"

type mark struct{}

func TestIDRoundTrip(t *testing.T) {
	id, err := NewID[mark]()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseID[mark](id.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != id {
		t.Fatalf("parsed = %s, want %s", parsed, id)
	}
	text, err := id.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var again ID[mark]
	if err := again.UnmarshalText(text); err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Fatalf("text = %s, want %s", again, id)
	}
	value, err := id.Value()
	if err != nil {
		t.Fatal(err)
	}
	var scanned ID[mark]
	if err := scanned.Scan(value); err != nil {
		t.Fatal(err)
	}
	if scanned != id {
		t.Fatalf("scan = %s, want %s", scanned, id)
	}
}
