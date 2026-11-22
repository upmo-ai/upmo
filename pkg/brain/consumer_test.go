package brain

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExternalModuleCompilation verifies that an external Go module importing
// only pkg/brain and the standard library compiles and passes go vet without
// referencing any internal/* packages.
func TestExternalModuleCompilation(t *testing.T) {
	consumerDir := filepath.Join("testdata", "consumer")
	// The consumer's go.mod pins the root module's dependencies. Vet against a
	// copy that go may update, so a root dependency bump does not also need a
	// hand edit here, and the checkout stays clean.
	modDir := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(consumerDir, name)) //nolint:gosec // fixed test paths
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(modDir, name), data, 0o600); err != nil { //nolint:gosec // fixed test paths
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "go", "vet", "-modfile="+filepath.Join(modDir, "go.mod"), "-mod=mod", "./...") //nolint:gosec // fixed arguments
	cmd.Dir = consumerDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("external consumer module failed go vet: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "use of internal package") {
		t.Fatalf("external consumer hit internal package leak:\n%s", out.String())
	}
}
