package blob

import (
	"context"
	"io"
	"os"
	"testing"
)

func TestVolumePutOpenRemove(t *testing.T) {
	root := t.TempDir()
	v := Volume{Root: root}
	key := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := v.Put(context.Background(), key, []byte("pdf-bytes")); err != nil {
		t.Fatal(err)
	}
	r, err := v.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(got) != "pdf-bytes" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := os.Stat(root + "/" + key); err != nil {
		t.Fatal(err)
	}
	if err := v.Remove(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(context.Background(), key); err != ErrNoBlob {
		t.Fatalf("err %v", err)
	}
}

func TestVolumeRejectsEscape(t *testing.T) {
	v := Volume{Root: t.TempDir()}
	if err := v.Put(context.Background(), "../x", []byte("no")); err != ErrBlob {
		t.Fatalf("err %v", err)
	}
	if err := v.Put(context.Background(), "/abs", []byte("no")); err != ErrBlob {
		t.Fatalf("err %v", err)
	}
	if err := v.Put(context.Background(), "not-a-uuid", []byte("no")); err != ErrBlob {
		t.Fatalf("err %v", err)
	}
}

func TestVolumeEmptyRoot(t *testing.T) {
	if err := (Volume{}).Put(context.Background(), "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb", []byte("x")); err != ErrNoBlob {
		t.Fatalf("err %v", err)
	}
}
