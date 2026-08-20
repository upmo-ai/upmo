package fake

import (
	"context"
	"fmt"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Adapter is the in-process provider used in tests and local compose.
type Adapter struct {
	YesNo  bool
	Choice string
	Reply  string
}

func (a Adapter) Decide(_ context.Context, _ app.Attempt, req domain.DecideRequest) (domain.DecideResult, domain.FailureClass, error) {
	switch req.Kind {
	case domain.DecisionYesNo:
		return domain.DecideResult{YesNo: a.YesNo}, domain.ClassOK, nil
	case domain.DecisionChoice:
		pick := a.Choice
		if pick == "" && len(req.Options) > 0 {
			pick = req.Options[0]
		}
		got, err := domain.ParseChoice(pick, req.Options)
		if err != nil {
			return domain.DecideResult{}, domain.ClassCaller, err
		}
		return domain.DecideResult{Choice: got}, domain.ClassOK, nil
	default:
		return domain.DecideResult{}, domain.ClassCaller, fmt.Errorf("gateway: decide kind %q", req.Kind)
	}
}

func (a Adapter) Chat(_ context.Context, _ app.Attempt, prompt string) (string, domain.FailureClass, error) {
	if a.Reply != "" {
		return a.Reply, domain.ClassOK, nil
	}
	return strings.TrimSpace(prompt), domain.ClassOK, nil
}
