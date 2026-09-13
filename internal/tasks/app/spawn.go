package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Subtask is one child of a task.
type Subtask struct {
	ID     string
	Status domain.Status
	Goal   string
}

// Spawner stores a child, lists children, and sends a line to a child.
type Spawner interface {
	Spawn(ctx context.Context, access Access, parentID, goal string, budget int, allow string) (Task, error)
	Children(ctx context.Context, access Access, parentID string) ([]Subtask, error)
	Send(ctx context.Context, access Access, taskID, body string) error
}

// Spawn opens one child under a parent. It does not admit a run.
func Spawn(ctx context.Context, book Spawner, access Access, parentID, goal string, budget int, allow string) (Task, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Task{}, ErrAccess
	}
	if _, err := uuid.Parse(parentID); err != nil {
		return Task{}, ErrTask
	}
	if _, _, err := domain.Step("", domain.Input{Kind: domain.Open, Goal: goal}); err != nil {
		return Task{}, err
	}
	if err := domain.BudgetFits(domain.TemplateBudgetMax, 0, budget); err != nil {
		return Task{}, err
	}
	return book.Spawn(ctx, access, parentID, goal, budget, allow)
}

// Await returns the children once every one is Done or Failed. It does not wait.
func Await(ctx context.Context, book Spawner, access Access, parentID string) ([]Subtask, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return nil, ErrAccess
	}
	if _, err := uuid.Parse(parentID); err != nil {
		return nil, ErrTask
	}
	children, err := book.Children(ctx, access, parentID)
	if err != nil {
		return nil, err
	}
	if children == nil {
		children = []Subtask{}
	}
	for _, child := range children {
		if child.Status != domain.Done && child.Status != domain.Failed {
			return nil, domain.ErrWaiting
		}
	}
	return children, nil
}

// Send appends one chat line on a child. The child's status stays.
func Send(ctx context.Context, book Spawner, access Access, taskID, body string) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	if _, _, err := domain.Step(domain.Preparing, domain.Input{Kind: domain.Chat, Body: body}); err != nil {
		return err
	}
	return book.Send(ctx, access, taskID, body)
}

// Cancel fails that one task. It does not fail a parent or a sibling.
func Cancel(ctx context.Context, book Book, access Access, taskID string) (Task, error) {
	return ApplyTransition(ctx, book, access, taskID, domain.Input{Kind: domain.Fail})
}
