package app

import (
	"errors"
	"time"
)

const (
	// WorkerTimeout is the longest a runner turn may run before timing out.
	WorkerTimeout = 10 * time.Minute
	// ClaimLease is how long a runner claim lease stays live before rescue takeover.
	ClaimLease = 11 * time.Minute
)

var (
	// ErrAlready indicates a turn or settlement has already been committed.
	ErrAlready = errors.New("tasks: already committed")
	// ErrBusy indicates another worker holds a live claim lease on the task.
	ErrBusy = errors.New("tasks: busy")
	// ErrFenced indicates the caller's claim token does not match or its lease expired.
	ErrFenced = errors.New("tasks: fenced")
	// ErrNotRunnable indicates the task or run is not runnable for the requested operation.
	ErrNotRunnable = errors.New("tasks: not runnable")
	// ErrReserve indicates a budget reservation was refused and the task was paused.
	ErrReserve = errors.New("tasks: reserve refused")
	// ErrReserved indicates a reservation is already held on the task.
	ErrReserved = errors.New("tasks: already reserved")
)
