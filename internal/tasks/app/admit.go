package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

var ErrRun = errors.New("tasks: run")

// Run is the state of one task run.
type Run struct {
	ID     string
	TaskID string
	State  domain.RunState
}

// Gate admits, ends, and lists runs for one person.
type Gate interface {
	Admit(ctx context.Context, access Access, taskID, runID string) (Run, error)
	End(ctx context.Context, access Access, runID string) (Run, error)
	Queued(ctx context.Context, access Access) ([]string, error)
}

// Admit validates the caller and delegates the only run admission check.
func Admit(ctx context.Context, gate Gate, access Access, taskID, runID string) (Run, error) {
	if !validRunAccess(access) {
		return Run{}, ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return Run{}, ErrTask
	}
	if runID != "" {
		if _, err := uuid.Parse(runID); err != nil {
			return Run{}, ErrRun
		}
	}
	return gate.Admit(ctx, access, taskID, runID)
}

// EndRun validates the caller and ends one run.
func EndRun(ctx context.Context, gate Gate, access Access, runID string) (Run, error) {
	if !validRunAccess(access) {
		return Run{}, ErrAccess
	}
	if _, err := uuid.Parse(runID); err != nil {
		return Run{}, ErrRun
	}
	return gate.End(ctx, access, runID)
}

// Sweep admits queued runs oldest first until the first run remains queued.
func Sweep(ctx context.Context, gate Gate, access Access) ([]Run, error) {
	if !validRunAccess(access) {
		return nil, ErrAccess
	}
	ids, err := gate.Queued(ctx, access)
	if err != nil {
		return nil, err
	}
	admitted := make([]Run, 0, len(ids))
	for _, id := range ids {
		run, err := gate.Admit(ctx, access, "", id)
		if err != nil {
			return nil, err
		}
		if run.State == domain.RunQueued {
			break
		}
		if run.State == domain.RunAdmitted {
			admitted = append(admitted, run)
		}
	}
	return admitted, nil
}

func validRunAccess(access Access) bool {
	_, err := uuid.Parse(access.UserID)
	return err == nil && access.Allow != ""
}
