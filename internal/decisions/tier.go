package decisions

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"
)

// RouteTier is the choice route that selects a caller-offered model tier.
const RouteTier = "model-tier"

var tierNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Canary is one task goal, its offered tiers, and the reference tier's outcome.
type Canary struct {
	Goal      string
	Tiers     []string
	Reference string
	Succeeded bool
}

// TierRoute accepts a model-tier config when routed success meets reference success parity.
func TierRoute(ctx context.Context, book Gates, access Access, hops []Hop, bar int, canaries []Canary, run Runner) error {
	checked, err := validateCanaries(canaries)
	if err != nil {
		return err
	}

	cfg := Config{
		Name: RouteTier,
		Kind: KindChoice,
		Hops: hops,
	}
	fingerprint, unchanged, err := prepared(ctx, book, access, cfg)
	if err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	if run == nil {
		return ErrConfig
	}

	var hit, total int
	for _, canary := range checked {
		yes, choice, err := run.Decide(
			ctx,
			canary.Goal,
			append([]string(nil), canary.Tiers...),
		)
		if err != nil {
			return err
		}

		choice = strings.TrimSpace(choice)
		if !containsTier(canary.Tiers, choice) {
			return ErrGate
		}
		if canary.Succeeded {
			total++
			if choice == canary.Reference || yes {
				hit++
			}
		}
	}

	return keep(ctx, book, access, cfg, fingerprint, bar, hit, total)
}

func validateCanaries(canaries []Canary) ([]Canary, error) {
	if len(canaries) == 0 {
		return nil, ErrConfig
	}

	checked := make([]Canary, len(canaries))
	hasReferenceSuccess := false
	for i, canary := range canaries {
		goal := strings.TrimSpace(canary.Goal)
		if runes := utf8.RuneCountInString(goal); runes < 1 || runes > 200 {
			return nil, ErrConfig
		}
		if len(canary.Tiers) < 2 || len(canary.Tiers) > 8 {
			return nil, ErrConfig
		}

		tiers := make([]string, len(canary.Tiers))
		tierSet := make(map[string]struct{}, len(canary.Tiers))
		for j, name := range canary.Tiers {
			name = strings.TrimSpace(name)
			if !tierNamePattern.MatchString(name) {
				return nil, ErrConfig
			}
			if _, duplicate := tierSet[name]; duplicate {
				return nil, ErrConfig
			}
			tiers[j] = name
			tierSet[name] = struct{}{}
		}

		reference := strings.TrimSpace(canary.Reference)
		if _, ok := tierSet[reference]; !ok {
			return nil, ErrConfig
		}
		hasReferenceSuccess = hasReferenceSuccess || canary.Succeeded
		checked[i] = Canary{
			Goal:      goal,
			Tiers:     tiers,
			Reference: reference,
			Succeeded: canary.Succeeded,
		}
	}
	if !hasReferenceSuccess {
		return nil, ErrConfig
	}
	return checked, nil
}

func containsTier(tiers []string, choice string) bool {
	for _, tier := range tiers {
		if tier == choice {
			return true
		}
	}
	return false
}
