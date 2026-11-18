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
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "report.pdf"), []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := (artifacts.Volume{Root: root}).Open(context.Background(), artifacts.StoreVolume, "task/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
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
	if _, err := v.Open(context.Background(), artifacts.StoreVolume, "."); !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("dot: %v", err)
	}
	if _, err := v.Open(context.Background(), artifacts.StoreVolume, "..foo"); !errors.Is(err, artifacts.ErrNoBlob) {
		t.Fatalf("dotdot-foo: %v", err)
	}
	if _, err := v.Open(context.Background(), artifacts.StoreVolume, "missing"); !errors.Is(err, artifacts.ErrNoBlob) {
		t.Fatalf("missing: %v", err)
	}
}

func TestVolume_UPM222_PutOpenRemove(t *testing.T) {
	root := t.TempDir()
	v := artifacts.Volume{Root: root}
	ctx := context.Background()
	const key = "task/report.pdf"
	if err := v.Put(ctx, key, []byte("pdf")); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(root, "task", "report.pdf")
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(full + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp left behind: %v", err)
	}
	body, err := v.Open(ctx, artifacts.StoreVolume, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(got) != "pdf" {
		t.Fatalf("body = %q err %v", got, err)
	}
	if err := v.Remove(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(ctx, artifacts.StoreVolume, key); !errors.Is(err, artifacts.ErrNoBlob) {
		t.Fatalf("after remove: %v", err)
	}
	if err := v.Remove(ctx, key); err != nil {
		t.Fatalf("missing remove: %v", err)
	}
	if err := v.Remove(ctx, "  "); err != nil {
		t.Fatalf("empty remove: %v", err)
	}
	if err := v.Put(ctx, ".", []byte("x")); !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("put dot: %v", err)
	}
	if err := v.Put(ctx, "..foo", []byte("ok")); err != nil {
		t.Fatalf("put ..foo: %v", err)
	}
	dotFoo, err := v.Open(ctx, artifacts.StoreVolume, "..foo")
	if err != nil {
		t.Fatal(err)
	}
	gotFoo, err := io.ReadAll(dotFoo)
	_ = dotFoo.Close()
	if err != nil || string(gotFoo) != "ok" {
		t.Fatalf("..foo body = %q err %v", gotFoo, err)
	}
	if err := v.Put(ctx, "../secret", []byte("x")); !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("put traversal: %v", err)
	}
	if err := v.Remove(ctx, "../secret"); !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("remove traversal: %v", err)
	}
	if _, err := v.Open(ctx, artifacts.StoreObject, key); !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("object store: %v", err)
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
