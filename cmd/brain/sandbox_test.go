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
	seats, err := sandboxSeats(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if seats.Ready() {
		t.Fatal("ready with UPMO_SANDBOXD_URL unset")
	}
	if _, err := seats.Provision(context.Background(), "018f3b2c-7a10-7c4a-8e11-111111111111", "org/name", nil); !errors.Is(err, runtime.ErrSandboxdUnset) {
		t.Fatalf("provision = %v", err)
	}
}

func TestSandboxSeatsWithURLOpensTheClient(t *testing.T) {
	raw := make([]byte, 32)
	key := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
	seats, err := sandboxSeats(config.Config{SandboxdURL: "http://sandboxd:8083", ServiceKey: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !seats.Ready() {
		t.Fatal("not ready with UPMO_SANDBOXD_URL set")
	}
}

func TestSandboxSeatsWithURLNeedsTheServiceKey(t *testing.T) {
	if _, err := sandboxSeats(config.Config{SandboxdURL: "http://sandboxd:8083"}, nil); err == nil {
		t.Fatal("opened sandboxd with no service key")
	}
}
