package decisions

import (
	"context"
	"strings"
	"unicode/utf8"
)

// RouteConfidence is the decide route that checks whether child outputs are sound.
const RouteConfidence = "result-confidence"

// Output is one child output labelled with whether it is sound.
type Output struct {
	Summary string
	Detail  string
	Yes     bool
}

// Confidence accepts a result-confidence config when its labelled precision meets the bar.
func Confidence(ctx context.Context, book Gates, access Access, hops []Hop, bar int, outputs []Output, run Runner) error {
	if len(outputs) == 0 {
		return ErrConfig
	}
	prompts := make([]string, len(outputs))
	for i, output := range outputs {
		summary := strings.TrimSpace(output.Summary)
		if runes := utf8.RuneCountInString(summary); runes < 1 || runes > 200 {
			return ErrConfig
		}
		detail := strings.TrimSpace(output.Detail)
		if utf8.RuneCountInString(detail) > 2000 {
			return ErrConfig
		}
		prompts[i] = summary
		if detail != "" {
			prompts[i] += "\n" + detail
		}
	}

	cfg := Config{
		Name: RouteConfidence,
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
	for i, prompt := range prompts {
		yes, _, err := run.Decide(ctx, prompt, nil)
		if err != nil {
			return err
		}
		if yes {
			total++
			if outputs[i].Yes { //nolint:gosec // prompts is built with len(outputs)
				hit++
			}
		}
	}
	return keep(ctx, book, access, cfg, fingerprint, bar, hit, total)
}
