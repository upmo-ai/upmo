package brain

import (
	"bytes"
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
	cmd := exec.CommandContext(t.Context(), "go", "vet", "./...")
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
