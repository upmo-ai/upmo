package decisions

import (
	"context"
	"strings"
	"unicode/utf8"
)

// RouteMemory is the decide route that checks whether a turn should read memory.
const RouteMemory = "memory-check"

// Turn is one prompt labelled with whether it should read memory.
type Turn struct {
	Prompt string
	Yes    bool
}

// MemoryCheck accepts a memory-check config when its labelled precision meets the bar.
func MemoryCheck(ctx context.Context, book Gates, access Access, hops []Hop, bar int, turns []Turn, run Runner) error {
	if len(turns) == 0 {
		return ErrConfig
	}
	examples := make([]Example, len(turns))
	for i, turn := range turns {
		prompt := strings.TrimSpace(turn.Prompt)
		if runes := utf8.RuneCountInString(prompt); runes < 1 || runes > 4000 {
			return ErrConfig
		}
		examples[i] = Example{
			Prompt:  prompt,
			Options: nil,
			Yes:     turn.Yes,
			Choice:  "",
		}
	}

	cfg := Config{
		Name: RouteMemory,
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
		if yes {
			total++
			if example.Yes {
				hit++
			}
		}
	}
	return keep(ctx, book, access, cfg, fingerprint, bar, hit, total)
}
