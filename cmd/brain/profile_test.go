package main

import (
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/memory"
)

func TestProfileLandsInTheCachePrefix(t *testing.T) {
	system := memory.SystemPrefix("Be brief.", "Ada lives in Dhaka")
	req, err := gateway.Build(gateway.Input{
		System:   system,
		Messages: []gateway.Message{{Role: gateway.RoleUser, Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.System, "Profile:\nAda lives in Dhaka") {
		t.Fatalf("system = %q", req.System)
	}
	cached := false
	for _, mark := range req.Cache {
		if mark == gateway.BreakSystem {
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
