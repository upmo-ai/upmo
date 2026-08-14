package archtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestModule enforces the import rules on this repository.
func TestModule(t *testing.T) {
	pkgs, err := Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no packages loaded")
	}
	for _, v := range Check("github.com/upmo-ai/upmo", pkgs) {
		t.Error(v)
	}
}

// TestFixtureViolations proves the check fails on planted forbidden imports
// and stays quiet on allowed ones.
func TestFixtureViolations(t *testing.T) {
	pkgs, err := Load("testdata/fixture")
	if err != nil {
		t.Fatal(err)
	}
	const m = "example.com/fixture/"
	want := []Violation{
		{m + "internal/memory", m + "internal/tasks/domain", "only the facade of context tasks is importable"},
		{m + "internal/memory/domain", m + "internal/memory/app", "domain reaches app"},
		{m + "internal/platform/web", m + "cmd/tool/flags", "cmd is not importable"},
		{m + "internal/tasks/domain", "database/sql", "domain imports an I/O package"},
		{m + "internal/tasks/domain", m + "internal/tasks/adapters/postgres", "domain reaches adapters"},
	}
	got := Check("example.com/fixture", pkgs)
	for _, v := range want {
		if !slices.Contains(got, v) {
			t.Errorf("missing violation: %s", v)
		}
	}
	for _, v := range got {
		if !slices.Contains(want, v) {
			t.Errorf("unexpected violation: %s", v)
		}
	}
}

func TestRoleSwitchStaysInPoolSetup(t *testing.T) {
	root := "../.."
	allow := map[string]bool{
		"cmd/brain/main.go":                  true,
		"internal/testutil/pgtest/pgtest.go": true,
	}
	needles := []string{"SET ROLE", "RESET ROLE", "row_security", "ROW LEVEL SECURITY"}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if allow[filepath.ToSlash(rel)] {
			return nil
		}
		if strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, ".sql") {
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		body, err := os.ReadFile(path) //nolint:gosec // path is from a walk of this module
		if err != nil {
			return err
		}
		text := string(body)
		for _, needle := range needles {
			if strings.Contains(text, needle) {
				t.Errorf("%s contains %q", rel, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
