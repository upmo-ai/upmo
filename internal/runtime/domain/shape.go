package domain

import (
	"errors"
	"time"
)

const (
	// ScratchName names the scratch volume a sandbox writes to.
	ScratchName = "scratch"
	// CPULimit is the steady CPU count of a sandbox.
	CPULimit = 1
	// CPUBurst is the CPU count a sandbox may burst to.
	CPUBurst = 2
	// PIDLimit caps the processes in a sandbox.
	PIDLimit = 256
	// ExecCap is the longest a single command may run.
	ExecCap = 15 * time.Minute
)

// ErrShape reports a command duration that is negative or longer than ExecCap.
var ErrShape = errors.New("runtime: shape")

// Shape is the fixed resource shape of a sandbox.
type Shape struct {
	Scratch string
	CPU     int
	Burst   int
	PIDs    int
	Exec    time.Duration
}

// ResourceShape returns the one shape every sandbox gets; nothing can widen it.
func ResourceShape() Shape {
	return Shape{Scratch: ScratchName, CPU: CPULimit, Burst: CPUBurst, PIDs: PIDLimit, Exec: ExecCap}
}

// AllowExec refuses a command duration that is negative or longer than ExecCap.
func AllowExec(d time.Duration) error {
	if d < 0 || d > ExecCap {
		return ErrShape
	}
	return nil
}
