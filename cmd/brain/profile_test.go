package main

import (
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/memory/app"
)

func TestProfileLandsInTheCachePrefix(t *testing.T) {
	system := app.SystemPrefix("Be brief.", "Ada lives in Dhaka")
	req, err := domain.Build(domain.Input{
		System:   system,
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.System, "Profile:\nAda lives in Dhaka") {
		t.Fatalf("system = %q", req.System)
	}
	cached := false
	for _, mark := range req.Cache {
		if mark == domain.BreakSystem {
			cached = true
		}
	}
	if !cached {
		t.Fatalf("cache = %v", req.Cache)
	}
	for _, msg := range req.Messages {
		if strings.Contains(msg.Text, "Dhaka") {
			t.Fatal("profile leaked into the turn")
		}
	}
}
