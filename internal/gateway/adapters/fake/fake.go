package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// The model ids the fake adapter scripts. A browser check picks one to choose
// how the stream behaves; any other id keeps the fields below. Only this
// adapter reads them: a real provider's model list never holds one.
const (
	ModelOK            = domain.ScriptedPrefix + "ok"
	ModelSlow          = domain.ScriptedPrefix + "slow"
	ModelFailRateLimit = domain.ScriptedPrefix + "fail-ratelimit"
	ModelFailAfterTwo  = domain.ScriptedPrefix + "fail-after-2"
	ModelToolCall      = domain.ScriptedPrefix + "tool-call"
	ModelUsage         = domain.ScriptedPrefix + "usage"
)

// UsageScript is what ModelUsage reports, so a test can tell every counter
// apart on its way to the caller and the ledger.
var UsageScript = domain.Usage{Arrived: true, Input: 100, Output: 50, CacheRead: 25, CacheWrite: 10, Reasoning: 5}

// scriptedText follows the "<model> says:" opening of every scripted reply.
const scriptedText = "this reply is scripted so the browser checks can read it whole."

// slowDelay is the pause between the words of the slow model.
const slowDelay = 300 * time.Millisecond

// Adapter is the in-process provider used in tests and local compose.
type Adapter struct {
	YesNo     bool
	Choice    string
	Reply     string
	ToolCalls []domain.ToolCall
	Usage     domain.Usage
	// Stream scripting: the words of Reply stream one per StreamDelay tick,
	// and when StreamFailClass is set the stream fails with it after
	// StreamFailWords words, zero failing before the first word.
	StreamDelay     time.Duration
	StreamFailWords int
	StreamFailClass domain.FailureClass
	// Models overrides the model list returned by Probe when non-nil.
	Models []domain.SeenModel
}

// script returns the adapter as the model's script asks it to behave, and the
// adapter itself for an id that has none.
func (a Adapter) script(model string, req domain.Request) Adapter {
	switch model {
	case ModelOK:
		a.Reply = model + " says: " + scriptedText
	case ModelSlow:
		a.Reply = model + " says: " + scriptedText
		a.StreamDelay = slowDelay
	case ModelFailRateLimit:
		a.Reply = model + " says: " + scriptedText
		a.StreamFailClass, a.StreamFailWords = domain.ClassRateLimit, 0
	case ModelFailAfterTwo:
		a.Reply = model + " says: " + scriptedText
		a.StreamFailClass, a.StreamFailWords = domain.ClassTransient, 2
	case ModelUsage:
		a.Reply = fmt.Sprintf("max_output_tokens=%d tool_choice=%s:%s", req.MaxOutputTokens, req.ToolChoice.Mode, req.ToolChoice.Name)
		a.Usage = UsageScript
	case ModelToolCall:
		if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == domain.RoleTool {
			last := req.Messages[n-1]
			prefix := "tool_result: "
			if last.IsError {
				prefix = "tool_error: "
			}
			a.Reply = prefix + last.Text
			a.ToolCalls = nil
			return a
		}
		name := "echo"
		for _, t := range req.Tools {
			if t.Name != "retrieve_output" && t.Name != "find_tool" {
				name = t.Name
				break
			}
		}
		a.Reply = ""
		a.ToolCalls = []domain.ToolCall{{
			ID:        "call_fake_1",
			Name:      name,
			Arguments: json.RawMessage(`{"input":"hi"}`),
		}}
	}
	return a
}

func (a Adapter) usageOr(fallback domain.Usage) domain.Usage {
	if a.Usage.Arrived {
		return a.Usage
	}
	return fallback
}

func (a Adapter) Decide(_ context.Context, attempt app.Attempt, req domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error) {
	a = a.script(attempt.Model, domain.Request{})
	usage := a.usageOr(domain.Usage{Arrived: true, Input: 1, Output: 1})
	switch req.Kind {
	case domain.DecisionYesNo:
		return domain.DecideResult{YesNo: a.YesNo}, usage, domain.ClassOK, nil
	case domain.DecisionChoice:
		pick := a.Choice
		if pick == "" && len(req.Options) > 0 {
			pick = req.Options[0]
		}
		got, err := domain.ParseChoice(pick, req.Options)
		if err != nil {
			return domain.DecideResult{}, domain.Usage{}, domain.ClassCaller, err
		}
		return domain.DecideResult{Choice: got}, usage, domain.ClassOK, nil
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

func (a Adapter) Chat(_ context.Context, attempt app.Attempt, req domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	a = a.script(attempt.Model, req)
	usage := a.usageOr(domain.Usage{Arrived: true, Input: 1, Output: 1})
	if len(a.ToolCalls) > 0 {
		return domain.ChatResult{
			Text:       a.Reply,
			ToolCalls:  cloneToolCalls(a.ToolCalls),
			StopReason: "tool_calls",
			Usage:      usage,
			Model:      attempt.Model,
		}, domain.ClassOK, nil
	}
	if a.Reply != "" {
		return domain.ChatResult{
			Text:       a.Reply,
			StopReason: "stop",
			Usage:      usage,
			Model:      attempt.Model,
		}, domain.ClassOK, nil
	}
	if len(req.Messages) == 0 {
		return domain.ChatResult{}, domain.ClassCaller, fmt.Errorf("gateway: chat prompt is empty")
	}
	return domain.ChatResult{
		Text:       strings.TrimSpace(req.Messages[len(req.Messages)-1].Text),
		StopReason: "stop",
		Usage:      usage,
		Model:      attempt.Model,
	}, domain.ClassOK, nil
}

// ChatStream streams the words of Reply one at a time, pausing StreamDelay
// between words. Usage counts the words that were sent, so a failed stream
// carries its partial usage. A scripted model id sets all of that itself.
func (a Adapter) ChatStream(ctx context.Context, attempt app.Attempt, req domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	res, class, err := a.ChatStreamTools(ctx, attempt, req, app.StreamEvents{Text: emit})
	return res.Usage, class, err
}

// ChatStreamTools streams text words and configured tool calls in order.
func (a Adapter) ChatStreamTools(ctx context.Context, attempt app.Attempt, req domain.Request, events app.StreamEvents) (domain.ChatResult, domain.FailureClass, error) {
	a = a.script(attempt.Model, req)
	words := strings.Fields(a.Reply)
	part := func(sent int) domain.Usage { return domain.Usage{Arrived: true, Input: 1, Output: sent} }
	sent := 0
	var text strings.Builder
	for _, word := range words {
		if a.StreamFailClass != "" && sent == a.StreamFailWords {
			return domain.ChatResult{Text: text.String(), Usage: part(sent), Model: attempt.Model},
				a.StreamFailClass, fmt.Errorf("gateway: fake stream %s", a.StreamFailClass)
		}
		if a.StreamDelay > 0 {
			select {
			case <-time.After(a.StreamDelay):
			case <-ctx.Done():
				return domain.ChatResult{Text: text.String(), Usage: part(sent), Model: attempt.Model},
					domain.ClassCanceled, ctx.Err()
			}
		}
		chunk := word + " "
		if events.Text != nil {
			if err := events.Text(chunk); err != nil {
				return domain.ChatResult{Text: text.String(), Usage: part(sent), Model: attempt.Model},
					domain.ClassCanceled, err
			}
		}
		text.WriteString(chunk)
		sent++
	}
	if a.StreamFailClass != "" && sent == a.StreamFailWords {
		return domain.ChatResult{Text: text.String(), Usage: part(sent), Model: attempt.Model},
			a.StreamFailClass, fmt.Errorf("gateway: fake stream %s", a.StreamFailClass)
	}
	calls := cloneToolCalls(a.ToolCalls)
	for i, tc := range calls {
		if events.ToolCall != nil {
			if err := events.ToolCall(app.ToolCallDelta{Index: i, ID: tc.ID, Name: tc.Name}); err != nil {
				return domain.ChatResult{Text: text.String(), Usage: part(sent), Model: attempt.Model},
					domain.ClassCanceled, err
			}
			args := strings.TrimSpace(string(tc.Arguments))
			if args == "" {
				args = "{}"
			}
			if err := events.ToolCall(app.ToolCallDelta{Index: i, ArgumentsDelta: args}); err != nil {
				return domain.ChatResult{Text: text.String(), Usage: part(sent), Model: attempt.Model},
					domain.ClassCanceled, err
			}
		}
	}
	usage := a.usageOr(part(sent + len(calls)))
	stopReason := "stop"
	if len(calls) > 0 {
		stopReason = "tool_calls"
	}
	return domain.ChatResult{
		Text:       text.String(),
		ToolCalls:  calls,
		StopReason: stopReason,
		Usage:      usage,
		Model:      attempt.Model,
	}, domain.ClassOK, nil
}

func cloneToolCalls(in []domain.ToolCall) []domain.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.ToolCall, len(in))
	copy(out, in)
	return out
}

// Probe always works: the fake has no network.
func (a Adapter) Probe(context.Context, app.Attempt) ([]domain.SeenModel, domain.FailureClass, error) {
	if a.Models != nil {
		return slices.Clone(a.Models), domain.ClassOK, nil
	}
	return []domain.SeenModel{
		{ID: ModelOK, DisplayName: "Fake OK"},
		{ID: ModelSlow, DisplayName: "Fake Slow"},
		{ID: ModelToolCall, DisplayName: "Fake Tool Call"},
		{ID: ModelUsage, DisplayName: "Fake Usage"},
	}, domain.ClassOK, nil
}
