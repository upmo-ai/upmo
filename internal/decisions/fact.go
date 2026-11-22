package decisions

import (
	"context"
	"strings"
	"unicode/utf8"
)

// RouteFact is the decide route that checks whether a fact is worth keeping.
const RouteFact = "fact-keep"

// Fact is one claim labeled with whether it is worth keeping.
type Fact struct {
	Claim string
	Yes   bool
}

// FactKeep accepts a fact-keep config when its labeled recall meets the bar.
func FactKeep(ctx context.Context, book Gates, access Access, hops []Hop, bar int, facts []Fact, run Runner) error {
	if len(facts) == 0 {
		return ErrConfig
	}
	examples := make([]Example, len(facts))
	for i, fact := range facts {
		claim := strings.TrimSpace(fact.Claim)
		if runes := utf8.RuneCountInString(claim); runes < 1 || runes > 1000 {
			return ErrConfig
		}
		examples[i] = Example{
			Prompt:  claim,
			Options: nil,
			Yes:     fact.Yes,
			Choice:  "",
		}
	}

	cfg := Config{
		Name: RouteFact,
		Kind: KindYesNo,
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
	for _, example := range examples {
		yes, _, err := run.Decide(ctx, example.Prompt, nil)
		if err != nil {
			return err
		}
		if example.Yes {
			total++
			if yes {
				hit++
			}
		}
	}
	return keep(ctx, book, access, cfg, fingerprint, bar, hit, total)
}
