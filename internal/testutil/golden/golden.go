// Package golden compares test output with text files under testdata.
package golden

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testing parses flags before tests run, so this flag is read from tests.
var update = flag.Bool("update", false, "rewrite golden files")

// String compares got with testdata/<name>.golden in the caller's package.
// -update rewrites the file.
//
// The file is text. A trailing newline is kept, and a rewrite does not add
// another, so the file stays stable.
func String(t *testing.T, name, got string) {
	t.Helper()
	path, err := goldenPath(name)
	if err != nil {
		t.Fatal(err)
	}
	got = newline(got)
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		// #nosec G304 -- path is testdata/<name>.golden and name cannot escape it
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	// #nosec G304 -- path is testdata/<name>.golden and name cannot escape it
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantText := newline(string(want))
	if got != wantText {
		t.Fatalf("golden %s: got %q, want %q", name, got, wantText)
	}
}

func goldenPath(name string) (string, error) {
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("golden: invalid name %q", name)
	}
	return filepath.Join("testdata", name+".golden"), nil
}

// newline appends a trailing newline when s has none. An existing trailing
// newline is left in place so rewriting a golden file does not grow it.
func newline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
