package domain

import (
	"errors"
	"strings"
	"time"
)

const (
	// ModeReadOnly mounts a volume so the editor cannot write to it.
	ModeReadOnly = "readonly"
	// ModeReadWrite mounts a volume so the editor can write to it.
	ModeReadWrite = "readwrite"
	// EditorIdle is how long an editor may sit idle before it stops.
	EditorIdle = 15 * time.Minute
	// EditorCap is the most editors a task may hold at once.
	EditorCap = 1
)

// ErrEditor reports a status with no editor mount, a negative idle time, an editor over the cap, or a token, origin, or gallery that does not match.
var ErrEditor = errors.New("runtime: editor")

// Mount is how an editor sees the scratch volume.
type Mount struct {
	Volume string
	Mode   string
}

// MountRule returns the scratch mount for a task status, and never takes a host path.
func MountRule(status string) (Mount, error) {
	switch strings.TrimSpace(status) {
	case "Working":
		return Mount{Volume: ScratchName, Mode: ModeReadOnly}, nil
	case "Checking", "Done", "Failed":
		return Mount{Volume: ScratchName, Mode: ModeReadWrite}, nil
	default:
		// Preparing, Planning, a blank status, and any other word have no editor mount.
		return Mount{}, ErrEditor
	}
}

// IdleStop reports whether an editor idle for idle should stop, and refuses a negative idle time.
func IdleStop(idle time.Duration) (bool, error) {
	if idle < 0 {
		return false, ErrEditor
	}
	return idle >= EditorIdle, nil
}

// AdmitEditor refuses another editor when a task already holds open editors at or over EditorCap.
func AdmitEditor(open int) error {
	if open < 0 || open >= EditorCap {
		return ErrEditor
	}
	return nil
}
