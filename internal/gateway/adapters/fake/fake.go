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

func (a Adapter) Decide(_ context.Context, _ app.Attempt, req domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error) {
	switch req.Kind {
	case domain.DecisionYesNo:
		return domain.DecideResult{YesNo: a.YesNo}, domain.Usage{}, domain.ClassOK, nil
	case domain.DecisionChoice:
		pick := a.Choice
		if pick == "" && len(req.Options) > 0 {
			pick = req.Options[0]
		}
		got, err := domain.ParseChoice(pick, req.Options)
		if err != nil {
			return domain.DecideResult{}, domain.Usage{}, domain.ClassCaller, err
		}
		return domain.DecideResult{Choice: got}, domain.Usage{}, domain.ClassOK, nil
	default:
		return domain.DecideResult{}, domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: decide kind %q", req.Kind)
	}
}

func (a Adapter) Embed(_ context.Context, _ app.Attempt, inputs []string) ([][]float32, domain.Usage, domain.FailureClass, error) {
	if err := domain.CheckEmbedInputs(inputs); err != nil {
		return nil, domain.Usage{}, domain.ClassCaller, err
	}
	out := make([][]float32, len(inputs))
	for i, text := range inputs {
		v := make([]float32, domain.EmbedDim)
		v[i%domain.EmbedDim] = 1
		v[int(text[0])%domain.EmbedDim] = 1
		out[i] = v
	}
	return out, domain.Usage{Arrived: true, Input: len(inputs)}, domain.ClassOK, nil
}

func (a Adapter) Chat(_ context.Context, _ app.Attempt, req domain.Request) (string, domain.Usage, domain.FailureClass, error) {
	if a.Reply != "" {
		return a.Reply, domain.Usage{}, domain.ClassOK, nil
	}
	if len(req.Messages) == 0 {
		return "", domain.Usage{}, domain.ClassCaller, fmt.Errorf("gateway: chat prompt is empty")
	}
	return strings.TrimSpace(req.Messages[len(req.Messages)-1].Text), domain.Usage{}, domain.ClassOK, nil
}

// Probe always works: the fake has no network.
func (a Adapter) Probe(context.Context, app.Attempt) (domain.FailureClass, error) {
	return domain.ClassOK, nil
}
