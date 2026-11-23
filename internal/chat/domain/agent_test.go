package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestAgentNameIsOneToSixtyCharactersWithNoControls(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"plain", "Research", "Research", true},
		{"trimmed", "  Research  ", "Research", true},
		{"empty", "", "", false},
		{"blank", "   ", "", false},
		{"longest", strings.Repeat("é", AgentNameRunes), strings.Repeat("é", AgentNameRunes), true},
		{"too long", strings.Repeat("n", AgentNameRunes+1), "", false},
		{"control character", "Re\nsearch", "", false},
		{"not utf-8", "Re\xffsearch", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AgentName(tc.in)
			var invalid Invalid
			if tc.ok != (err == nil) || (!tc.ok && !errors.As(err, &invalid)) || got != tc.want {
				t.Fatalf("AgentName(%q) = %q, %v", tc.in, got, err)
			}
		})
	}
}

func TestAgentOverlayKeepsLineBreaksAndIsBounded(t *testing.T) {
	if got, err := AgentOverlay("  Be brief.\n\n- Cite sources.\tAlways.  "); err != nil || got != "Be brief.\n\n- Cite sources.\tAlways." {
		t.Fatalf("overlay = %q %v", got, err)
	}
	if got, err := AgentOverlay(""); err != nil || got != "" {
		t.Fatalf("empty overlay = %q %v", got, err)
	}
	if _, err := AgentOverlay(strings.Repeat("o", AgentOverlayRunes)); err != nil {
		t.Fatalf("longest overlay: %v", err)
	}
	for name, in := range map[string]string{
		"too long": strings.Repeat("o", AgentOverlayRunes+1),
		"bell":     "ring\a",
		"null":     "a\x00b",
	} {
		if _, err := AgentOverlay(in); err == nil {
			t.Fatalf("%s: overlay accepted", name)
		}
	}
}

func TestAgentDescriptionIsBounded(t *testing.T) {
	if got, err := AgentDescription(" Finds things. "); err != nil || got != "Finds things." {
		t.Fatalf("description = %q %v", got, err)
	}
	if _, err := AgentDescription(strings.Repeat("d", AgentDescriptionRunes+1)); err == nil {
		t.Fatal("a description over the limit was accepted")
	}
	if _, err := AgentDescription("a\tb"); err == nil {
		t.Fatal("a description with a tab was accepted")
	}
}

func TestAgentRouteIsEmptyOrARouteName(t *testing.T) {
	if got, err := AgentRoute(" fast "); err != nil || got != "fast" {
		t.Fatalf("route = %q %v", got, err)
	}
	if got, err := AgentRoute(""); err != nil || got != "" {
		t.Fatalf("empty route = %q %v", got, err)
	}
	for _, bad := range []string{"has space", "-lead", strings.Repeat("r", 65), "a/b"} {
		var invalid Invalid
		if _, err := AgentRoute(bad); !errors.As(err, &invalid) {
			t.Fatalf("route %q: %v, want a plain refusal", bad, err)
		}
	}
}

func TestAgentModelNeedsProviderAndModelOrNeither(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model string
		ok                    bool
	}{
		{"neither", "", "", true},
		{"both", "OpenAI", "gpt-x", true},
		{"blank both", " ", " ", true},
		{"provider only", "OpenAI", "", false},
		{"model only", "", "gpt-x", false},
		{"provider too long", strings.Repeat("p", AgentProviderRunes+1), "gpt-x", false},
		{"model too long", "OpenAI", strings.Repeat("m", PinModelRunes+1), false},
		{"longest model", "OpenAI", strings.Repeat("m", PinModelRunes), true},
		{"control in provider", "Open\nAI", "gpt-x", false},
		{"control in model", "OpenAI", "gpt\tx", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := AgentModel(tc.provider, tc.model)
			var invalid Invalid
			if tc.ok != (err == nil) || (!tc.ok && !errors.As(err, &invalid)) {
				t.Fatalf("AgentModel(%q, %q) = %v, want ok=%v", tc.provider, tc.model, err, tc.ok)
			}
		})
	}
	p, m, err := AgentModel(" OpenAI ", " gpt-x ")
	if err != nil || p != "OpenAI" || m != "gpt-x" {
		t.Fatalf("trimmed = %q %q %v", p, m, err)
	}
}

func TestResolveSettlesTheRouteAndTheModelEachOnItsOwn(t *testing.T) {
	agent := &Defaults{Route: "deep", ModelProvider: "OpenAI", ModelID: "gpt-x"}
	routeOnly := &Defaults{Route: "deep"}
	modelOnly := &Defaults{ModelProvider: "OpenAI", ModelID: "gpt-x"}
	const account = "018f3b2c-7a10-7c4a-8e11-111111111111"
	for _, tc := range []struct {
		name    string
		picked  Picked
		agent   *Defaults
		install string
		want    Resolved
	}{
		{"nothing at all", Picked{}, nil, "chat", Resolved{Route: "chat"}},
		{"composer route over agent route", Picked{Route: "fast"}, agent, "chat",
			Resolved{Route: "fast", PinProvider: "OpenAI", PinModel: "gpt-x"}},
		{"agent route over install", Picked{}, routeOnly, "chat", Resolved{Route: "deep"}},
		{"install route when neither", Picked{}, modelOnly, "chat",
			Resolved{Route: "chat", PinProvider: "OpenAI", PinModel: "gpt-x"}},
		{"composer pin over agent model", Picked{PinAccount: account, PinModel: "gpt-y"}, agent, "chat",
			Resolved{Route: "deep", PinAccount: account, PinModel: "gpt-y"}},
		{"agent model when the composer pinned none", Picked{Route: "fast"}, modelOnly, "chat",
			Resolved{Route: "fast", PinProvider: "OpenAI", PinModel: "gpt-x"}},
		{"composer pin without an agent", Picked{PinAccount: account, PinModel: "gpt-y"}, nil, "chat",
			Resolved{Route: "chat", PinAccount: account, PinModel: "gpt-y"}},
		{"no pin leaves the chain alone", Picked{Route: "fast"}, routeOnly, "chat", Resolved{Route: "fast"}},
		{"half an agent model is none", Picked{}, &Defaults{ModelProvider: "OpenAI"}, "chat", Resolved{Route: "chat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.picked, tc.agent, tc.install); got != tc.want {
				t.Fatalf("Resolve = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReasonOfAnAgentFailureIsPlain(t *testing.T) {
	if got := Reason(ClassAgent); got != "that agent is not available" {
		t.Fatalf("reason = %q", got)
	}
}
