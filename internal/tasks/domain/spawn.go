package domain

import "errors"

const (
	// TreeLevels is a parent and its children. A child cannot spawn.
	TreeLevels = 2
	// Fanout is the most children one parent may have, including finished ones.
	Fanout = 5
	// ChildCap is the most open children in the install. It sits one above
	// Fanout so a full parent and the shared cap can both be seen.
	ChildCap = 6
)

var (
	// ErrDepth is a spawn from a task that already has a parent.
	ErrDepth = errors.New("tasks: depth")
	// ErrFanout is a parent that already has Fanout children.
	ErrFanout = errors.New("tasks: fanout")
	// ErrCeiling is the install-wide cap on open children.
	ErrCeiling = errors.New("tasks: ceiling")
	// ErrWaiting is a parent that still has an open child.
	ErrWaiting = errors.New("tasks: waiting")
)

// Ceilings reports which spawn limit holds. depth is the parent's level,
// starting at 1 for a task with no parent. siblings counts every child.
// openChildren counts children that are not Done or Failed, for every person.
func Ceilings(depth, siblings, openChildren int) error {
	if depth >= TreeLevels {
		return ErrDepth
	}
	if siblings >= Fanout {
		return ErrFanout
	}
	if openChildren >= ChildCap {
		return ErrCeiling
	}
	return nil
}
