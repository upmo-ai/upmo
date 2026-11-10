package fake

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Adapter is the in-process provider used in tests and local compose.
type Adapter struct {
	YesNo  bool
	Choice string
	Reply  string
	// Stream scripting: the words of Reply stream one per StreamDelay tick,
	// and when StreamFailClass is set the stream fails with it after
	// StreamFailWords words, zero failing before the first word.
	StreamDelay     time.Duration
	StreamFailWords int
	StreamFailClass domain.FailureClass
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

// ChatStream streams the words of Reply one at a time, pausing StreamDelay
// between words. Usage counts the words that were sent, so a failed stream
// carries its partial usage.
func (a Adapter) ChatStream(ctx context.Context, _ app.Attempt, _ domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	words := strings.Fields(a.Reply)
	part := func(sent int) domain.Usage { return domain.Usage{Arrived: true, Input: 1, Output: sent} }
	sent := 0
	for _, word := range words {
		if a.StreamFailClass != "" && sent == a.StreamFailWords {
			return part(sent), a.StreamFailClass, fmt.Errorf("gateway: fake stream %s", a.StreamFailClass)
		}
		if a.StreamDelay > 0 {
			select {
			case <-time.After(a.StreamDelay):
			case <-ctx.Done():
				return part(sent), domain.ClassCancelled, ctx.Err()
			}
		}
		if err := emit(word + " "); err != nil {
			return part(sent), domain.ClassCancelled, err
		}
		sent++
	}
	if a.StreamFailClass != "" && sent == a.StreamFailWords {
		return part(sent), a.StreamFailClass, fmt.Errorf("gateway: fake stream %s", a.StreamFailClass)
	}
	return part(sent), domain.ClassOK, nil
}

// Probe always works: the fake has no network.
func (a Adapter) Probe(context.Context, app.Attempt) (domain.FailureClass, error) {
	return domain.ClassOK, nil
}
