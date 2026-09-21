package decisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/google/uuid"
)

const (
	KindYesNo   = "yesno"
	KindChoice  = "choice"
	KindFilter  = "filter"
	KindRank    = "rank"
	VerdictPass = "pass"
	VerdictFail = "fail"
)

var (
	// ErrAccess is a caller who cannot manage settings.
	ErrAccess = errors.New("decisions: access")
	// ErrConfig is a decide route the gate cannot fingerprint.
	ErrConfig = errors.New("decisions: config")
	// ErrGate is a model change the labelled cases did not pass.
	ErrGate = errors.New("decisions: gate")
)

// Hop is one model on a decide route. The fingerprint covers these fields.
type Hop struct {
	Position int    `json:"position"`
	Adapter  string `json:"adapter"`
	Model    string `json:"model"`
	Weight   int    `json:"weight"`
	Retry    int    `json:"retry"`
}

// Config is the decide route the gate fingerprints.
type Config struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Hops []Hop  `json:"hops"`
}

// Example is one labelled case. Yes is read for yesno. Choice is read for choice.
type Example struct {
	Prompt  string
	Options []string
	Yes     bool
	Choice  string
}

// Runner is the decide call. The gate does not talk to a model itself.
type Runner interface {
	Decide(ctx context.Context, prompt string, options []string) (yes bool, choice string, err error)
}

// Access is the caller. Managing a gate needs settings:manage.
type Access struct {
	UserID string
	Allow  string
}

// Fingerprint is the hex SHA-256 of the route name, kind and hops.
// Hop order in the input does not change it. A model change does.
func Fingerprint(cfg Config) (string, error) {
	if cfg.Name == "" || (cfg.Kind != KindYesNo && cfg.Kind != KindChoice && cfg.Kind != KindFilter && cfg.Kind != KindRank) || len(cfg.Hops) == 0 {
		return "", ErrConfig
	}
	hops := append([]Hop(nil), cfg.Hops...)
	sort.Slice(hops, func(i, j int) bool { return hops[i].Position < hops[j].Position })
	seen := map[int]struct{}{}
	for _, hop := range hops {
		if hop.Position < 1 || hop.Adapter == "" || hop.Model == "" || hop.Weight < 1 || hop.Retry < 0 {
			return "", ErrConfig
		}
		if _, ok := seen[hop.Position]; ok {
			return "", ErrConfig
		}
		seen[hop.Position] = struct{}{}
	}
	cfg.Hops = hops
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Verdict passes when the hit rate meets the bar. The bar is a percent from 1 to 100.
func Verdict(hit, total, bar int) (string, error) {
	if total < 1 || hit < 0 || hit > total || bar < 1 || bar > 100 {
		return "", ErrGate
	}
	if hit*100 >= total*bar {
		return VerdictPass, nil
	}
	return VerdictFail, nil
}

// Evaluate runs the labelled cases. A runner error stops the gate and counts as nothing.
func Evaluate(ctx context.Context, run Runner, kind string, examples []Example) (hit, total int, err error) {
	if run == nil || len(examples) == 0 || (kind != KindYesNo && kind != KindChoice) {
		return 0, 0, ErrConfig
	}
	for _, example := range examples {
		if example.Prompt == "" {
			return 0, 0, ErrConfig
		}
		yes, choice, err := run.Decide(ctx, example.Prompt, example.Options)
		if err != nil {
			return 0, 0, err
		}
		switch kind {
		case KindYesNo:
			if len(example.Options) != 0 {
				return 0, 0, ErrConfig
			}
			if yes == example.Yes {
				hit++
			}
		case KindChoice:
			if len(example.Options) < 2 || example.Choice == "" {
				return 0, 0, ErrConfig
			}
			if choice == example.Choice {
				hit++
			}
		}
	}
	return hit, len(examples), nil
}

// Gates remembers the last config a route was allowed to use.
type Gates interface {
	Accepted(ctx context.Context, access Access, route string) (fingerprint string, ok bool, err error)
	Save(ctx context.Context, access Access, row Gate) error
}

// Gate is one passing eval for a route.
type Gate struct {
	Route       string
	Kind        string
	Fingerprint string
	Hit         int
	Total       int
	Bar         int
}

// Accept stores the route when its fingerprint is unchanged, or when the labelled cases pass for the new one.
// A failed gate leaves the stored fingerprint as it was.
func Accept(ctx context.Context, book Gates, access Access, cfg Config, bar int, examples []Example, run Runner) error {
	next, unchanged, err := prepared(ctx, book, access, cfg)
	if err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	hit, total, err := Evaluate(ctx, run, cfg.Kind, examples)
	if err != nil {
		return err
	}
	return keep(ctx, book, access, cfg, next, bar, hit, total)
}

func prepared(ctx context.Context, book Gates, access Access, cfg Config) (fingerprint string, unchanged bool, err error) {
	if _, err := uuid.Parse(access.UserID); err != nil || !allows(access.Allow, "settings:manage") {
		return "", false, ErrAccess
	}
	next, err := Fingerprint(cfg)
	if err != nil {
		return "", false, err
	}
	stored, ok, err := book.Accepted(ctx, access, cfg.Name)
	if err != nil {
		return "", false, err
	}
	if ok && stored == next {
		return next, true, nil
	}
	return next, false, nil
}

func keep(ctx context.Context, book Gates, access Access, cfg Config, fingerprint string, bar, hit, total int) error {
	verdict, err := Verdict(hit, total, bar)
	if err != nil {
		return err
	}
	if verdict != VerdictPass {
		return ErrGate
	}
	return book.Save(ctx, access, Gate{
		Route:       cfg.Name,
		Kind:        cfg.Kind,
		Fingerprint: fingerprint,
		Hit:         hit,
		Total:       total,
		Bar:         bar,
	})
}

func allows(list, name string) bool {
	for _, part := range splitComma(list) {
		if part == name {
			return true
		}
	}
	return false
}

func splitComma(list string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(list); i++ {
		if i == len(list) || list[i] == ',' {
			part := trim(list[start:i])
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
