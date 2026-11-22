package app

import (
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

// TurnRoute is where every round of one reply goes: the route, the chat it
// is billed to, and the pin, as settled by the reply worker's request.
type TurnRoute struct {
	Route           string
	ChatID          string
	CredentialRef   string
	PinAccount      string
	PinProvider     string
	PinModel        string
	MaxOutputTokens int
}

// Turns opens the model of a multi-round reply.
type Turns interface {
	Bind(route TurnRoute, onDelta func(string), onAttempt func(ModelAttempt)) agentloop.ModelClient
}
