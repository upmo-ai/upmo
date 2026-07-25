package golden

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewline(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "\n"},
		{"a", "a\n"},
		{"a\n", "a\n"},
		{"a\n\n", "a\n\n"},
	}
	for _, tt := range tests {
		if got := newline(tt.in); got != tt.want {
			t.Fatalf("newline(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestGoldenPath(t *testing.T) {
	if _, err := goldenPath("sample"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "..", "../x", "a/b", `a\b`} {
		if _, err := goldenPath(name); err == nil {
			t.Errorf("goldenPath(%q) error = nil", name)
		}
	}
}

func TestStringUpdateIsStable(t *testing.T) {
	t.Chdir(t.TempDir())
	prev := *update
	t.Cleanup(func() { *update = prev })

	*update = true
	String(t, "sample", "hello")
	String(t, "sample", "hello")

	got, err := os.ReadFile(filepath.Join("testdata", "sample.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("golden file = %q, want %q", got, "hello\n")
	}

	*update = false
	String(t, "sample", "hello")
	String(t, "sample", "hello\n")
}
