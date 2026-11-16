package buildinfo

import (
	"encoding/json"
	"runtime/debug"
	"testing"
)

func TestResolveInfoStampedValuesOverrideVCS(t *testing.T) {
	bi := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "from-vcs"},
			{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
		},
	}
	got := resolveInfo("v1.2.3", "abc123", "2026-11-16T08:00:00Z", func() (*debug.BuildInfo, bool) {
		return bi, true
	})
	if got.Version != "v1.2.3" || got.Commit != "abc123" || got.BuildTime != "2026-11-16T08:00:00Z" {
		t.Fatalf("got = %+v", got)
	}
}

func TestResolveInfoFallsBackToVCS(t *testing.T) {
	bi := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "deadbeef"},
			{Key: "vcs.time", Value: "2026-11-15T12:00:00Z"},
		},
	}
	got := resolveInfo("dev", "unknown", "unknown", func() (*debug.BuildInfo, bool) {
		return bi, true
	})
	if got.Version != "dev" || got.Commit != "deadbeef" || got.BuildTime != "2026-11-15T12:00:00Z" {
		t.Fatalf("got = %+v", got)
	}
}

func TestResolveInfoDefaultsWhenUnavailable(t *testing.T) {
	got := resolveInfo("", "", "", func() (*debug.BuildInfo, bool) {
		return nil, false
	})
	if got.Version != "dev" || got.Commit != "unknown" || got.BuildTime != "unknown" {
		t.Fatalf("got = %+v", got)
	}
}

func TestResponseJSONTags(t *testing.T) {
	resp := Response{
		Version:        "v1.0.0",
		Commit:         "abc123",
		BuildTime:      "2026-11-16T08:00:00Z",
		SchemaVersions: map[string]int64{"audit": 2, "identity": 3},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "commit", "build_time", "schema_versions"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing JSON key %q in %s", key, raw)
		}
	}
}
