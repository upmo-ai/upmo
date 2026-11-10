package gateway

import (
	"context"
	"errors"

	"github.com/upmo-ai/upmo/internal/chat/app"
)

// Model is the client as the reply job's model: it maps the job's request
// onto a gateway call and the gateway's failures onto *app.ModelError. A
// stream that was cut, or a call that failed before the gateway answered, is
// a transient failure of no model in particular.
type Model struct{ Client Client }

// Stream implements app.Model.
func (m Model) Stream(ctx context.Context, req app.ModelRequest, onDelta func(string), onAttempt func(app.ModelAttempt)) (app.ModelDone, error) {
	messages := make([]Message, 0, len(req.Messages))
	for _, message := range req.Messages {
		messages = append(messages, Message{Role: message.Role, Text: message.Text})
	}
	done, err := m.Client.ChatStream(ctx, Request{
		Route:           req.Route,
		CredentialRef:   req.CredentialRef,
		UserID:          req.UserID,
		Messages:        messages,
		MaxOutputTokens: req.MaxOutputTokens,
		PinAccount:      req.PinAccount,
		PinModel:        req.PinModel,
		AgentID:         req.AgentID,
	}, onDelta, func(a Attempt) {
		if onAttempt != nil {
			onAttempt(app.ModelAttempt{FailedModel: a.FailedModel, Class: a.FailureClass, NextModel: a.NextModel})
		}
	})
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			return app.ModelDone{}, &app.ModelError{Class: failure.Class, Model: failure.Model}
		}
		return app.ModelDone{}, err
	}
	return app.ModelDone{
		Model:        done.Model,
		InputTokens:  done.InputTokens,
		OutputTokens: done.OutputTokens,
		CostUSD:      done.CostUSD,
	}, nil
}
