package domain

import (
	"errors"
	"testing"
)

func TestPlaceRefusesUnknownOrUnmet(t *testing.T) {
	cases := []struct{ tier, platform string }{
		{"", PlatformCompose},
		{TierGVisor, ""},
		{"microvm", PlatformKubernetes},
		{"sandbox", PlatformCompose},
		{TierGVisor, "pod"},
		{TierKata, PlatformCompose},
	}
	for _, c := range cases {
		got, err := Place(c.tier, c.platform)
		if !errors.Is(err, ErrTier) {
			t.Fatalf("Place(%q, %q) error = %v, want %v", c.tier, c.platform, err, ErrTier)
		}
		if got != (Placement{}) {
			t.Fatalf("Place(%q, %q) = %+v, want zero Placement", c.tier, c.platform, got)
		}
	}
}

func TestPlaceMeetsTier(t *testing.T) {
	cases := []struct {
		tier, platform string
		want           Placement
	}{
		{" GVisor ", " Compose ", Placement{Tier: TierGVisor, Runtime: RuntimeRunsc}},
		{" GVisor ", " Kubernetes ", Placement{Tier: TierGVisor, RuntimeClass: RuntimeClassGVisor}},
		{"Kata", "Kubernetes", Placement{Tier: TierKata, RuntimeClass: RuntimeClassKata}},
		{"container", PlatformCompose, Placement{Tier: TierContainer}},
		{"container", PlatformKubernetes, Placement{Tier: TierContainer}},
	}
	for _, c := range cases {
		got, err := Place(c.tier, c.platform)
		if err != nil {
			t.Fatalf("Place(%q, %q) error = %v", c.tier, c.platform, err)
		}
		if got != c.want {
			t.Fatalf("Place(%q, %q) = %+v, want %+v", c.tier, c.platform, got, c.want)
		}
	}
}
