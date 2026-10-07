package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime"
)

func TestTaskSandboxesWithoutURLHasNoProvider(t *testing.T) {
	boxes, err := taskSandboxes(config.Config{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if boxes.Ready() {
		t.Fatal("ready with UPMO_SANDBOXD_URL unset")
	}
	if boxes.Store == nil {
		t.Fatal("no task sandbox store")
	}
	ctx := context.Background()
	if _, err := boxes.Provision(ctx, "018f3b2c-7a10-7c4a-8e11-111111111111", "org/name", nil, nil); !errors.Is(err, runtime.ErrSandboxdUnset) {
		t.Fatalf("provision = %v", err)
	}
	// With no sandboxd there is nothing to destroy, so the task sandbox store is not read.
	if err := boxes.Release(ctx, "018f3b2c-7a10-7c4a-8e11-111111111111"); err != nil {
		t.Fatalf("release = %v", err)
	}
}

func TestTaskSandboxesWithURLOpensTheClient(t *testing.T) {
	raw := make([]byte, 32)
	key := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
	boxes, err := taskSandboxes(config.Config{SandboxdURL: "http://sandboxd:8083", ServiceKey: key, SandboxImage: "upmo/sandbox:base"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !boxes.Ready() {
		t.Fatal("not ready with UPMO_SANDBOXD_URL set")
	}
	if boxes.Image != "upmo/sandbox:base" {
		t.Fatalf("image = %q", boxes.Image)
	}
}

func TestTaskSandboxesWithURLNeedsTheServiceKey(t *testing.T) {
	if _, err := taskSandboxes(config.Config{SandboxdURL: "http://sandboxd:8083"}, nil, nil); err == nil {
		t.Fatal("opened sandboxd with no service key")
	}
}
