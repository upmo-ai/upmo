package decisions

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	// RouteRisk is the choice route that reviews permission risk.
	RouteRisk = "permission-risk"
	// RiskAllow is the loosest permission verdict.
	RiskAllow = "allow"
	// RiskAsk is the permission verdict that requires confirmation.
	RiskAsk = "ask"
	// RiskBlock is the strictest permission verdict.
	RiskBlock = "block"
)

// Risk is one prompt, its prior verdict, and whether it is in the danger set.
type Risk struct {
	Prompt string
	Prior  string
	Danger bool
}

// SecondOpinion accepts a permission-risk config only when every second opinion preserves or tightens the prior verdict.
func SecondOpinion(ctx context.Context, book Gates, access Access, hops []Hop, cases []Risk, run Runner) error {
	checked, err := validateRisks(cases)
	if err != nil {
		return err
	}

	cfg := Config{
		Name: RouteRisk,
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

	for _, risk := range checked {
		_, choice, err := run.Decide(ctx, risk.Prompt, []string{RiskAllow, RiskAsk, RiskBlock})
		if err != nil {
			return err
		}
		choice = strings.TrimSpace(choice)
		choiceRank, ok := riskRank(choice)
		if !ok {
			return ErrGate
		}
		priorRank, _ := riskRank(risk.Prior)
		if choiceRank < priorRank || (risk.Danger && choice == RiskAllow) {
			return ErrGate
		}
	}

	return keep(ctx, book, access, cfg, fingerprint, 100, len(checked), len(checked))
}

func validateRisks(cases []Risk) ([]Risk, error) {
	if len(cases) == 0 {
		return nil, ErrConfig
	}

	checked := make([]Risk, len(cases))
	hasDanger := false
	for i, risk := range cases {
		prompt := strings.TrimSpace(risk.Prompt)
		if runes := utf8.RuneCountInString(prompt); runes < 1 || runes > 4000 {
			return nil, ErrConfig
		}
		prior := strings.TrimSpace(risk.Prior)
		if _, ok := riskRank(prior); !ok {
			return nil, ErrConfig
		}
		hasDanger = hasDanger || risk.Danger
		checked[i] = Risk{
			Prompt: prompt,
			Prior:  prior,
			Danger: risk.Danger,
		}
	}
	if !hasDanger {
		return nil, ErrConfig
	}
	return checked, nil
}

func riskRank(verdict string) (int, bool) {
	switch verdict {
	case RiskAllow:
		return 0, true
	case RiskAsk:
		return 1, true
	case RiskBlock:
		return 2, true
	default:
		return 0, false
	}
}
