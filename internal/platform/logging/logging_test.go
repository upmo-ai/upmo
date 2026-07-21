package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNewWritesJSONAtLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "warn")
	log.Info("hidden")
	log.Warn("shown", "k", "v")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("want one JSON record, got %q: %v", buf.String(), err)
	}
	if rec["msg"] != "shown" || rec["k"] != "v" {
		t.Errorf("unexpected record: %v", rec)
	}
}
