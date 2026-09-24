package domain

import (
	"errors"
	"strings"
	"time"
)

// IdleTTL is how long a seat may sit idle before its container is released.
const IdleTTL = 15 * time.Minute

// ErrSeat reports a blank container id, a negative idle duration, or a resume while a container is still held.
var ErrSeat = errors.New("runtime: seat")

// Seat holds the container a task occupies and whether the task is paused.
type Seat struct {
	Container string
	Paused    bool
}

// Hold returns an unpaused seat that holds the trimmed container id.
func Hold(id string) (Seat, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Seat{}, ErrSeat
	}

	return Seat{Container: id}, nil
}

// Pause returns the seat paused, with an empty container, and the id it released.
func Pause(seat Seat) (Seat, string) {
	id := seat.Container
	seat.Paused = true
	seat.Container = ""

	return seat, id
}

// Release returns the seat after the given idle duration and the id of any container it released.
func Release(seat Seat, idle time.Duration) (Seat, string, error) {
	if idle < 0 {
		return seat, "", ErrSeat
	}
	if seat.Container == "" {
		return seat, "", nil
	}
	if !seat.Paused && idle < IdleTTL {
		return seat, "", nil
	}

	id := seat.Container
	seat.Container = ""

	return seat, id, nil
}

// Resume returns the seat unpaused with an empty container, a new provisioning record, and the id of any container it still held.
func Resume(seat Seat) (Seat, Record, string, error) {
	if !seat.Paused && seat.Container != "" {
		return seat, Record{}, "", ErrSeat
	}

	id := seat.Container
	seat.Paused = false
	seat.Container = ""

	return seat, Begin(), id, nil
}
