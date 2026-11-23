package gateway

import (
	"context"
	"errors"
	"strings"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

// Turns is the gateway as the model of a multi-round reply.
type Turns struct{ Client Client }

var _ app.Turns = Turns{}

// Bind returns the model of one reply: every Complete goes to route, streams
// its text to onDelta and its failovers to onAttempt. Both may be nil.
func (t Turns) Bind(route app.TurnRoute, onDelta func(string), onAttempt func(app.ModelAttempt)) agentloop.ModelClient {
	return turnModel{client: t.Client, route: route, onDelta: onDelta, onAttempt: onAttempt}
}

type turnModel struct {
	client    Client
	route     app.TurnRoute
	onDelta   func(string)
	onAttempt func(app.ModelAttempt)
}

// Complete runs one round of the reply on the bound route.
func (m turnModel) Complete(ctx context.Context, req agentloop.PromptRequest) (agentloop.PromptResponse, error) {
	tools := make([]Tool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, Tool{Name: tool.Name, Description: tool.Description, Schema: tool.Schema})
	}
	messages := make([]Message, 0, len(req.Messages))
	for _, message := range req.Messages {
		calls := make([]ToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			calls = append(calls, ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments, ThoughtSignature: call.ThoughtSignature})
		}
		messages = append(messages, Message{
			Role:      string(message.Role),
			Text:      message.Text,
			ToolCalls: calls,
			ToolName:  message.ToolName,
			ToolID:    message.ToolID,
			IsError:   message.IsError,
			Offload:   message.Offload,
		})
	}
	var text strings.Builder
	done, err := m.client.ChatStream(ctx, Request{
		Route:           m.route.Route,
		ChatID:          m.route.ChatID,
		CredentialRef:   m.route.CredentialRef,
		UserID:          req.UserID,
		AgentID:         req.AgentID,
		RunID:           req.RunID,
		TeamID:          req.TeamID,
		System:          req.System,
		Tools:           tools,
		CoreTools:       !m.route.DisableCoreTools,
		Messages:        messages,
		MaxOutputTokens: m.route.MaxOutputTokens,
		PinAccount:      m.route.PinAccount,
		PinProvider:     m.route.PinProvider,
		PinModel:        m.route.PinModel,
	}, func(delta string) {
		text.WriteString(delta)
		if m.onDelta != nil {
			m.onDelta(delta)
		}
	}, func(a Attempt) {
		if m.onAttempt != nil {
			m.onAttempt(app.ModelAttempt{FailedModel: a.FailedModel, Class: a.FailureClass, NextModel: a.NextModel})
		}
	})
	if err != nil {
		return agentloop.PromptResponse{}, modelError(err)
	}
	calls := make([]agentloop.ToolCall, 0, len(done.ToolCalls))
	for _, call := range done.ToolCalls {
		calls = append(calls, agentloop.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments, ThoughtSignature: call.ThoughtSignature})
	}
	return agentloop.PromptResponse{
		Text:       text.String(),
		ToolCalls:  calls,
		StopReason: done.StopReason,
		Model:      done.Model,
		Usage: kernel.TokenUsage{
			InputTokens:      int64(done.InputTokens),
			OutputTokens:     int64(done.OutputTokens),
			CacheReadTokens:  int64(done.CacheRead),
			CacheWriteTokens: int64(done.CacheWrite),
			ReasoningTokens:  int64(done.Reasoning),
		},
		CostMicroUSD:  done.CostMicroUSD,
		ContextWindow: done.ContextWindow,
	}, nil
}

// modelError maps a gateway failure onto *app.ModelError and keeps any other error.
func modelError(err error) error {
	var failure *Failure
	if errors.As(err, &failure) {
		return &app.ModelError{Class: failure.Class, Model: failure.Model}
	}
	return err
}
