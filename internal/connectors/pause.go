package connectors

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ErrPause reports a pause that cannot be held or resumed and never carries tool text.
var ErrPause = errors.New("connectors: pause")

// Reasons a connector call may be paused.
const (
	PauseExpired = "expired"
	PauseAsk     = "ask"
)

// maxPauseTool is the longest tool name a pause accepts, in bytes.
const maxPauseTool = 64

// Pause is one held connector call, bound to its user, run and id.
type Pause struct {
	ID, User, Run, Tool, Reason string
}

// Hold records a paused call. It neither mints a token nor dials.
func Hold(id, user, run, tool, reason string) (Pause, error) {
	id = strings.TrimSpace(id)
	user = strings.TrimSpace(user)
	run = strings.TrimSpace(run)
	tool = strings.TrimSpace(tool)
	reason = strings.TrimSpace(reason)

	i, ok := pauseUUID(id)
	if !ok {
		return Pause{}, ErrPause
	}
	u, ok := pauseUUID(user)
	if !ok {
		return Pause{}, ErrPause
	}
	r, ok := pauseUUID(run)
	if !ok {
		return Pause{}, ErrPause
	}
	if i == u || i == r || u == r {
		return Pause{}, ErrPause
	}
	if tool == "" || len(tool) > maxPauseTool || hasCredentialMark(tool) {
		return Pause{}, ErrPause
	}
	if reason != PauseExpired && reason != PauseAsk {
		return Pause{}, ErrPause
	}
	return Pause{ID: id, User: user, Run: run, Tool: tool, Reason: reason}, nil
}

// Resume lets a paused call continue only for its own user, run and id.
// It neither mints a token nor dials.
func Resume(p Pause, user, run, id string) error {
	// A pause that Hold would not return as is, such as the zero value, never resumes.
	held, err := Hold(p.ID, p.User, p.Run, p.Tool, p.Reason)
	if err != nil || held != p {
		return ErrPause
	}
	if strings.TrimSpace(user) != p.User || strings.TrimSpace(run) != p.Run || strings.TrimSpace(id) != p.ID {
		return ErrPause
	}
	return nil
}

// pauseUUID parses s and refuses the nil UUID.
func pauseUUID(s string) (uuid.UUID, bool) {
	v, err := uuid.Parse(s)
	if err != nil || v == uuid.Nil {
		return uuid.Nil, false
	}
	return v, true
}
