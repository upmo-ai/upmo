package artifacts_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/upmo-ai/upmo/internal/artifacts"
)

func TestVolumeOpensAKeyUnderRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "task")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "report.pdf"), []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := (artifacts.Volume{Root: root}).Open(context.Background(), artifacts.StoreVolume, "task/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "pdf" {
		t.Fatalf("body = %q err %v", got, err)
	}
}

func TestVolumeRefusesObjectStoreAndTraversal(t *testing.T) {
	root := t.TempDir()
	v := artifacts.Volume{Root: root}
	if _, err := v.Open(context.Background(), artifacts.StoreObject, "task/a"); !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("object: %v", err)
	}
	if _, err := v.Open(context.Background(), artifacts.StoreVolume, "../secret"); !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("traversal: %v", err)
	}
	if _, err := v.Open(context.Background(), artifacts.StoreVolume, "missing"); !errors.Is(err, artifacts.ErrNoBlob) {
		t.Fatalf("missing: %v", err)
	}
}

func TestMediaFromExtension(t *testing.T) {
	if got := artifacts.Media("report.pdf"); got != "application/pdf" {
		t.Fatalf("pdf = %q", got)
	}
	if got := artifacts.Media("notes"); got != artifacts.TypeBytes {
		t.Fatalf("none = %q", got)
	}
}
