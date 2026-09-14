package app

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/trustfence"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Automations stores a trigger and fires it into a template.
type Automations interface {
	SaveAutomation(ctx context.Context, access Access, templateID, kind, spec string) (string, error)
	Fire(ctx context.Context, access Access, automationID, dedup string, values map[string]string, sourceTask string) (string, bool, error)
	SetMode(ctx context.Context, access Access, automationID, mode string) error
	OwnerLeft(ctx context.Context, access Access, userID string) error
	Reassign(ctx context.Context, access Access, automationID, userID string) error
}

// SaveAutomation records a trigger that points at a template. It starts on.
func SaveAutomation(ctx context.Context, book Automations, access Access, templateID, kind, spec string) (string, error) {
	if err := automationAccess(access); err != nil {
		return "", err
	}
	if _, err := uuid.Parse(templateID); err != nil {
		return "", ErrTask
	}
	spec, err := domain.Trigger(kind, spec)
	if err != nil {
		return "", err
	}
	return book.SaveAutomation(ctx, access, templateID, kind, spec)
}

// Fire opens one task from the template. The same dedup key returns the existing task.
func Fire(ctx context.Context, book Automations, access Access, automationID, dedup string, values map[string]string, sourceTask string) (string, bool, error) {
	if err := automationAccess(access); err != nil {
		return "", false, err
	}
	if _, err := uuid.Parse(automationID); err != nil {
		return "", false, ErrTask
	}
	dedup, err := domain.DedupKey(dedup)
	if err != nil {
		return "", false, err
	}
	if sourceTask != "" {
		if _, err := uuid.Parse(sourceTask); err != nil {
			return "", false, ErrTask
		}
	}
	clean := make(map[string]string, len(values))
	for name, value := range values {
		clean[name] = strings.TrimSpace(trustfence.NeutralizeSlot(value))
	}
	return book.Fire(ctx, access, automationID, dedup, clean, sourceTask)
}

// SetMode turns an automation on or off. An owner who has left cannot turn it on.
func SetMode(ctx context.Context, book Automations, access Access, automationID, mode string) error {
	if err := automationAccess(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(automationID); err != nil {
		return ErrTask
	}
	if mode != domain.ModeOn && mode != domain.ModeOff {
		return domain.ErrMode
	}
	return book.SetMode(ctx, access, automationID, mode)
}

// OwnerLeft disables every automation that person owns.
func OwnerLeft(ctx context.Context, book Automations, access Access, userID string) error {
	if err := automationAccess(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(userID); err != nil {
		return ErrTask
	}
	if access.UserID != userID && !holds(access.Allow, "members:manage") {
		return ErrAccess
	}
	return book.OwnerLeft(ctx, access, userID)
}

// Reassign gives the automation to another person. An admin does this after the owner leaves.
func Reassign(ctx context.Context, book Automations, access Access, automationID, userID string) error {
	if err := automationAccess(access); err != nil {
		return err
	}
	if !holds(access.Allow, "members:manage") {
		return ErrAccess
	}
	if _, err := uuid.Parse(automationID); err != nil {
		return ErrTask
	}
	if _, err := uuid.Parse(userID); err != nil {
		return ErrTask
	}
	return book.Reassign(ctx, access, automationID, userID)
}

func automationAccess(access Access) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	return nil
}

func holds(list, name string) bool {
	for _, part := range strings.Split(list, ",") {
		if strings.TrimSpace(part) == name {
			return true
		}
	}
	return false
}
