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

func TestSandboxSeatsWithoutURLHasNoProvider(t *testing.T) {
	seats, err := sandboxSeats(config.Config{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if seats.Ready() {
		t.Fatal("ready with UPMO_SANDBOXD_URL unset")
	}
	if seats.Book == nil {
		t.Fatal("no seat book")
	}
	ctx := context.Background()
	if _, err := seats.Provision(ctx, "018f3b2c-7a10-7c4a-8e11-111111111111", "org/name", nil, nil); !errors.Is(err, runtime.ErrSandboxdUnset) {
		t.Fatalf("provision = %v", err)
	}
	// With no sandboxd there is nothing to destroy, so the seat book is not read.
	if err := seats.Release(ctx, "018f3b2c-7a10-7c4a-8e11-111111111111"); err != nil {
		t.Fatalf("release = %v", err)
	}
}

func TestSandboxSeatsWithURLOpensTheClient(t *testing.T) {
	raw := make([]byte, 32)
	key := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
	seats, err := sandboxSeats(config.Config{SandboxdURL: "http://sandboxd:8083", ServiceKey: key, SeatImage: "upmo/sandbox:base"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !seats.Ready() {
		t.Fatal("not ready with UPMO_SANDBOXD_URL set")
	}
	if seats.Image != "upmo/sandbox:base" {
		t.Fatalf("image = %q", seats.Image)
	}
}

func TestSandboxSeatsWithURLNeedsTheServiceKey(t *testing.T) {
	if _, err := sandboxSeats(config.Config{SandboxdURL: "http://sandboxd:8083"}, nil, nil); err == nil {
		t.Fatal("opened sandboxd with no service key")
	}
}
