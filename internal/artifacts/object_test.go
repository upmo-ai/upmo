package artifacts

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestObjectOpensViaGetter(t *testing.T) {
	obj := Object{
		Bucket: "bucket",
		get: func(_ context.Context, key string) (io.ReadCloser, error) {
			if key != "task/a.pdf" {
				t.Fatalf("key = %q", key)
			}
			return io.NopCloser(strings.NewReader("pdf")), nil
		},
	}
	body, err := obj.Open(context.Background(), StoreObject, "task/a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "pdf" {
		t.Fatalf("body = %q err %v", got, err)
	}
}

func TestObjectRefusesVolumeAndTraversal(t *testing.T) {
	called := false
	obj := Object{
		Bucket: "bucket",
		get: func(context.Context, string) (io.ReadCloser, error) {
			called = true
			return nil, nil
		},
	}
	if _, err := obj.Open(context.Background(), StoreVolume, "task/a"); !errors.Is(err, ErrBlob) {
		t.Fatalf("volume: %v", err)
	}
	if _, err := obj.Open(context.Background(), StoreObject, "../secret"); !errors.Is(err, ErrBlob) {
		t.Fatalf("traversal: %v", err)
	}
	if called {
		t.Fatal("getter called for refused keys")
	}
}

type stubBlobs struct {
	n    *int
	body string
}

func (s stubBlobs) Open(context.Context, string, string) (io.ReadCloser, error) {
	*s.n++
	return io.NopCloser(strings.NewReader(s.body)), nil
}

func TestMuxDispatches(t *testing.T) {
	rootCalls, objectCalls := 0, 0
	mux := Mux{
		Volume: stubBlobs{n: &rootCalls, body: "vol"},
		Object: stubBlobs{n: &objectCalls, body: "obj"},
	}
	body, err := mux.Open(context.Background(), StoreVolume, "a")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if string(got) != "vol" || rootCalls != 1 || objectCalls != 0 {
		t.Fatalf("volume route got=%q root=%d obj=%d", got, rootCalls, objectCalls)
	}
	body, err = mux.Open(context.Background(), StoreObject, "b")
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(body)
	body.Close()
	if string(got) != "obj" || objectCalls != 1 {
		t.Fatalf("object route got=%q obj=%d", got, objectCalls)
	}
}

func TestMuxObjectUnsetIsBlob(t *testing.T) {
	mux := Mux{Volume: Volume{Root: t.TempDir()}}
	if _, err := mux.Open(context.Background(), StoreObject, "a"); !errors.Is(err, ErrBlob) {
		t.Fatalf("err = %v", err)
	}
}
