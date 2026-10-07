package domain

import (
	"errors"
	"strings"
	"time"
)

// IdleTTL is how long a hold may sit idle before its container is released.
const IdleTTL = 15 * time.Minute

// ErrHold reports a blank container id, a negative idle duration, or a resume while a container is still held.
var ErrHold = errors.New("runtime: hold")

// Hold is the container a task occupies and whether the task is paused.
type Hold struct {
	Container string
	Paused    bool
}

// NewHold returns an unpaused hold that holds the trimmed container id.
func NewHold(id string) (Hold, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Hold{}, ErrHold
	}

	return Hold{Container: id}, nil
}

// Pause returns the hold paused, with an empty container, and the id it released.
func Pause(h Hold) (Hold, string) {
	id := h.Container
	h.Paused = true
	h.Container = ""

	return h, id
}

// Release returns the hold after the given idle duration and the id of any container it released.
func Release(h Hold, idle time.Duration) (Hold, string, error) {
	if idle < 0 {
		return h, "", ErrHold
	}
	if h.Container == "" {
		return h, "", nil
	}
	if !h.Paused && idle < IdleTTL {
		return h, "", nil
	}

	id := h.Container
	h.Container = ""

	return h, id, nil
}

// Resume returns the hold unpaused with an empty container, a new provisioning record, and the id of any container it still held.
func Resume(h Hold) (Hold, Record, string, error) {
	if !h.Paused && h.Container != "" {
		return h, Record{}, "", ErrHold
	}

	id := h.Container
	h.Paused = false
	h.Container = ""

	return h, Begin(), id, nil
}
