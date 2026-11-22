package decisions

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// RouteDispatch is the decide route that chooses an agent.
	RouteDispatch = "agent-dispatch"
	// RouteIntent is the decide route that chooses an intent.
	RouteIntent = "route-intent"
)

var intentName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,39}$`)

// Labeled is one prompt, its choices, and the expected choice.
type Labeled struct {
	Prompt  string
	Options []string
	Label   string
}

// Dispatch accepts an agent dispatch config when its labeled accuracy meets the bar.
func Dispatch(ctx context.Context, book Gates, access Access, hops []Hop, bar int, cases []Labeled, run Runner) error {
	return acceptLabeled(ctx, book, access, RouteDispatch, hops, bar, cases, run, validAgentID)
}

// Intent accepts an intent route config when its labeled accuracy meets the bar.
func Intent(ctx context.Context, book Gates, access Access, hops []Hop, bar int, cases []Labeled, run Runner) error {
	return acceptLabeled(ctx, book, access, RouteIntent, hops, bar, cases, run, validIntentName)
}

func acceptLabeled(
	ctx context.Context,
	book Gates,
	access Access,
	route string,
	hops []Hop,
	bar int,
	cases []Labeled,
	run Runner,
	validOption func(string) bool,
) error {
	if len(cases) == 0 {
		return ErrConfig
	}
	examples := make([]Example, len(cases))
	for i, labeled := range cases {
		prompt := strings.TrimSpace(labeled.Prompt)
		if runes := utf8.RuneCountInString(prompt); runes < 1 || runes > 2000 {
			return ErrConfig
		}
		if len(labeled.Options) < 2 || len(labeled.Options) > 8 {
			return ErrConfig
		}
		options := make([]string, len(labeled.Options))
		seen := make(map[string]struct{}, len(labeled.Options))
		for j, option := range labeled.Options {
			option = strings.TrimSpace(option)
			if runes := utf8.RuneCountInString(option); runes < 1 || runes > 80 || !validOption(option) {
				return ErrConfig
			}
			if _, ok := seen[option]; ok {
				return ErrConfig
			}
			seen[option] = struct{}{}
			options[j] = option
		}
		label := strings.TrimSpace(labeled.Label)
		if _, ok := seen[label]; !ok {
			return ErrConfig
		}
		examples[i] = Example{
			Prompt:  prompt,
			Options: options,
			Choice:  label,
		}
	}
	return Accept(ctx, book, access, Config{
		Name: route,
		Kind: KindChoice,
		Hops: hops,
	}, bar, examples, run)
}

func validAgentID(option string) bool {
	_, err := uuid.Parse(option)
	return err == nil
}

func validIntentName(option string) bool {
	return intentName.MatchString(option)
}
