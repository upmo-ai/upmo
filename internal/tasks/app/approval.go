package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// ErrNoApproval is an approval this person cannot see or that does not exist.
var ErrNoApproval = errors.New("tasks: no such approval")

// Approval is one stored approval as the person who decides it sees it. Title
// is the goal of its task.
type Approval struct {
	ID     string
	TaskID string
	Title  string
	Action string
	State  domain.ApprovalState
}

// ApprovalBook stores approvals. Request pauses a task and stores a pending
// approval in one transaction. Decide resumes it in one transaction under a
// row lock. Neither runs a timer or decides on its own.
type ApprovalBook interface {
	Request(ctx context.Context, access Access, taskID string, ask domain.Ask) (Approval, error)
	Decide(ctx context.Context, access Access, approvalID string, kind domain.Kind) (Approval, error)
	Approvals(ctx context.Context, access Access) ([]Approval, error)
}

// ApprovalsShown is the most approvals a list returns.
const ApprovalsShown = 50

// RequestApproval checks the request, then pauses the task (Working to Needs
// approval) and stores one pending approval for its owner. A task with a
// pending approval refuses another.
func RequestApproval(ctx context.Context, book ApprovalBook, access Access, taskID, tool, reason, action string) (Approval, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Approval{}, ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return Approval{}, ErrTask
	}
	ask, err := domain.NewAsk(tool, reason, action)
	if err != nil {
		return Approval{}, err
	}
	return book.Request(ctx, access, taskID, ask)
}

// DecideApproval approves or denies a pending approval and resumes its task.
// Kind is domain.Approve or domain.Deny.
func DecideApproval(ctx context.Context, book ApprovalBook, access Access, approvalID string, kind domain.Kind) (Approval, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Approval{}, ErrAccess
	}
	if kind != domain.Approve && kind != domain.Deny {
		return Approval{}, domain.ErrInput
	}
	if _, err := uuid.Parse(approvalID); err != nil {
		return Approval{}, ErrNoApproval
	}
	return book.Decide(ctx, access, approvalID, kind)
}

// ListApprovals returns this person's approvals: pending first, then the most
// recent decided ones, at most ApprovalsShown.
func ListApprovals(ctx context.Context, book ApprovalBook, access Access) ([]Approval, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return nil, ErrAccess
	}
	return book.Approvals(ctx, access)
}
