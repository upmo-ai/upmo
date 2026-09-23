//go:build !ignore

package image

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// moduleRoot walks up from this test file until it finds go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + file)
		}
		dir = parent
	}
}

func readFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(moduleRoot(t), rel)) //nolint:gosec // rel is a fixed path in this module
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDockerfile(t *testing.T) {
	df := readFile(t, "deploy/sandbox/Dockerfile")

	required := []struct {
		name    string
		pattern string
	}{
		{"base target", `(?mi)^FROM\s+\S+\s+AS\s+base\s*$`},
		{"cli target", `(?mi)^FROM\s+base\s+AS\s+cli\s*$`},
		{"MISE_DATA_DIR=/opt/mise", `(?m)^ENV\s+MISE_DATA_DIR=/opt/mise\s*$`},
		{"VOLUME /opt/mise", `(?m)^VOLUME\s+/opt/mise\s*$`},
	}
	for _, r := range required {
		if !regexp.MustCompile(r.pattern).MatchString(df) {
			t.Errorf("Dockerfile is missing %s", r.name)
		}
	}

	lower := strings.ToLower(df)
	for _, banned := range []string{"golang", "nodejs", "python3", "rustc", "cargo", "build-essential"} {
		if strings.Contains(lower, banned) {
			t.Errorf("Dockerfile contains %q; toolchains install on first use through mise", banned)
		}
	}
}

func TestEngines(t *testing.T) {
	got := strings.Split(strings.TrimSuffix(readFile(t, "deploy/sandbox/engines.txt"), "\n"), "\n")
	want := []string{"claude", "codex", "opencode", "cursor", "pi"}
	if !slices.Equal(got, want) {
		t.Errorf("engines.txt = %q, want %q", got, want)
	}
}
