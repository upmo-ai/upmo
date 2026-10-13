package archtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
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
		{m + "internal/platform/leak", m + "internal/sandboxd", "only cmd/sandboxd imports sandboxd"},
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

// TestCmdTestMain lets a cmd package's own generated test main import it,
// and still refuses any other importer of cmd.
func TestCmdTestMain(t *testing.T) {
	const m = "example.com/fixture"
	tool := &packages.Package{PkgPath: m + "/cmd/tool"}
	pkgs := []*packages.Package{
		{PkgPath: m + "/cmd/tool.test", Imports: map[string]*packages.Package{tool.PkgPath: tool}},
		{PkgPath: m + "/cmd/other.test", Imports: map[string]*packages.Package{tool.PkgPath: tool}},
	}
	want := []Violation{{m + "/cmd/other.test", m + "/cmd/tool", "cmd is not importable"}}
	if got := Check(m, pkgs); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestEnginePackages covers the engine rule, which the fixture cannot load
// without the engine modules.
func TestEnginePackages(t *testing.T) {
	for _, path := range []string{"github.com/docker/docker/client", "github.com/moby/moby", "github.com/containerd/containerd/v2/client", "k8s.io/client-go/kubernetes"} {
		if !isEngine(path) {
			t.Errorf("isEngine(%q) = false", path)
		}
	}
	for _, path := range []string{"github.com/dockerx", "net/http", "k8s.io/api"} {
		if isEngine(path) {
			t.Errorf("isEngine(%q) = true", path)
		}
	}
	const m = "example.com/fixture"
	for path, want := range map[string]bool{
		m + "/internal/sandboxd":              true,
		m + "/internal/sandboxd/driver":       true,
		m + "/internal/sandboxd.test":         true,
		m + "/cmd/sandboxd":                   true,
		m + "/internal/sandboxdx":             false,
		m + "/internal/runtime":               false,
		"example.com/other/internal/sandboxd": false,
	} {
		if got := inSandboxd(m, path); got != want {
			t.Errorf("inSandboxd(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestRoleSwitchStaysInPoolSetup(t *testing.T) {
	root := "../.."
	allowSetRole := map[string]bool{
		"cmd/brain/main.go":                  true,
		"internal/testutil/pgtest/pgtest.go": true,
	}
	revert := []string{"RESET ROLE", "SET ROLE NONE", "SET SESSION AUTHORIZATION"}
	needles := []string{"SET ROLE", "row_security", "ROW LEVEL SECURITY"}
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
		slash := filepath.ToSlash(rel)
		for _, needle := range revert {
			if strings.Contains(text, needle) {
				t.Errorf("%s contains %q", rel, needle)
			}
		}
		if !allowSetRole[slash] {
			for _, needle := range needles {
				if strings.Contains(text, needle) {
					t.Errorf("%s contains %q", rel, needle)
				}
			}
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "SET upmo.") {
				t.Errorf("%s sets an upmo option for the session", rel)
			}
			if strings.Contains(line, "set_config('upmo.") && strings.Contains(line, "false") {
				t.Errorf("%s writes an upmo setting that survives the transaction", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
