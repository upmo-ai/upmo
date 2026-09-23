package domain

import "errors"

const (
	// PhaseClone identifies the clone phase.
	PhaseClone Phase = "clone"
	// PhaseToolchain identifies the toolchain phase.
	PhaseToolchain Phase = "toolchain"
	// PhaseProbe identifies the probe phase.
	PhaseProbe Phase = "probe"
	// PhaseReady identifies a provisioned sandbox.
	PhaseReady Phase = "ready"
	// PhaseFailed identifies a failed provisioning.
	PhaseFailed Phase = "failed"
	// EventCap is the most events a provisioning record may hold.
	EventCap = 8
)

// Phase identifies a provisioning phase.
type Phase string

const (
	// KindNote records progress within the current phase.
	KindNote Kind = "note"
	// KindDone completes the current phase.
	KindDone Kind = "done"
	// KindFail fails the provisioning.
	KindFail Kind = "fail"
)

// Kind identifies a provisioning event.
type Kind string

// ErrProvision reports an event on a finished record or an unknown kind.
var ErrProvision = errors.New("runtime: provision")

// ErrCeiling reports an event that would pass EventCap.
var ErrCeiling = errors.New("runtime: ceiling")

// Record holds the provisioning phase and its event count.
type Record struct {
	Phase  Phase
	Events int
}

// Begin returns a record in the clone phase with one event.
func Begin() Record {
	return Record{Phase: PhaseClone, Events: 1}
}

// Apply returns the record after one event of the given kind.
func Apply(rec Record, kind Kind) (Record, error) {
	if rec.Phase == PhaseReady || rec.Phase == PhaseFailed {
		return rec, ErrProvision
	}
	if rec.Events >= EventCap {
		return rec, ErrCeiling
	}

	next := rec
	switch kind {
	case KindNote:
	case KindDone:
		switch rec.Phase {
		case PhaseClone:
			next.Phase = PhaseToolchain
		case PhaseToolchain:
			next.Phase = PhaseProbe
		case PhaseProbe:
			next.Phase = PhaseReady
		default:
			return rec, ErrProvision
		}
	case KindFail:
		next.Phase = PhaseFailed
	default:
		return rec, ErrProvision
	}
	next.Events++

	return next, nil
}
