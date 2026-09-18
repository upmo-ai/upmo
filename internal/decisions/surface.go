package decisions

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"
)

// RouteSurface is the filter route that checks a tool surface.
const RouteSurface = "tool-surface"

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Surface is one turn's prompt, offered tools and called tools.
type Surface struct {
	Prompt  string
	Offered []string
	Called  []string
}

// Filter keeps the tools that should remain available for a prompt.
type Filter interface {
	Keep(ctx context.Context, prompt string, offered []string) (kept []string, err error)
}

// Prefilter accepts a tool-surface config only when every surface keeps every called tool.
func Prefilter(ctx context.Context, book Gates, access Access, hops []Hop, surfaces []Surface, filter Filter) error {
	checked, err := validateSurfaces(surfaces)
	if err != nil {
		return err
	}

	cfg := Config{
		Name: RouteSurface,
		Kind: KindFilter,
		Hops: hops,
	}
	fingerprint, unchanged, err := prepared(ctx, book, access, cfg)
	if err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	if filter == nil {
		return ErrConfig
	}

	for _, surface := range checked {
		kept, err := filter.Keep(
			ctx,
			surface.Prompt,
			append([]string(nil), surface.Offered...),
		)
		if err != nil {
			return err
		}

		offered := make(map[string]struct{}, len(surface.Offered))
		for _, name := range surface.Offered {
			offered[name] = struct{}{}
		}
		keptSet := make(map[string]struct{}, len(kept))
		for _, name := range kept {
			name = strings.TrimSpace(name)
			if name == "" {
				return ErrGate
			}
			if _, ok := offered[name]; !ok {
				return ErrGate
			}
			keptSet[name] = struct{}{}
		}
		for _, name := range surface.Called {
			if _, ok := keptSet[name]; !ok {
				return ErrGate
			}
		}
	}

	return keep(ctx, book, access, cfg, fingerprint, 100, len(checked), len(checked))
}

func validateSurfaces(surfaces []Surface) ([]Surface, error) {
	if len(surfaces) == 0 {
		return nil, ErrConfig
	}

	checked := make([]Surface, len(surfaces))
	calledTotal := 0
	for i, surface := range surfaces {
		prompt := strings.TrimSpace(surface.Prompt)
		if runes := utf8.RuneCountInString(prompt); runes < 1 || runes > 4000 {
			return nil, ErrConfig
		}
		if len(surface.Offered) < 1 || len(surface.Offered) > 32 {
			return nil, ErrConfig
		}

		offered := make([]string, len(surface.Offered))
		offeredSet := make(map[string]struct{}, len(surface.Offered))
		for j, name := range surface.Offered {
			name = strings.TrimSpace(name)
			if !toolNamePattern.MatchString(name) {
				return nil, ErrConfig
			}
			if _, duplicate := offeredSet[name]; duplicate {
				return nil, ErrConfig
			}
			offered[j] = name
			offeredSet[name] = struct{}{}
		}

		called := make([]string, len(surface.Called))
		calledSet := make(map[string]struct{}, len(surface.Called))
		for j, name := range surface.Called {
			name = strings.TrimSpace(name)
			if _, duplicate := calledSet[name]; duplicate {
				return nil, ErrConfig
			}
			if _, ok := offeredSet[name]; !ok {
				return nil, ErrConfig
			}
			called[j] = name
			calledSet[name] = struct{}{}
		}
		calledTotal += len(called)
		checked[i] = Surface{
			Prompt:  prompt,
			Offered: offered,
			Called:  called,
		}
	}
	if calledTotal == 0 {
		return nil, ErrConfig
	}
	return checked, nil
}
