package app

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// TemplateBook saves, opens and removes templates under row security.
type TemplateBook interface {
	SaveTemplate(ctx context.Context, access Access, id string, template domain.Template) (string, error)
	OpenTemplate(ctx context.Context, access Access, id string) (domain.Template, error)
	RemoveTemplate(ctx context.Context, access Access, id string) error
}

// SaveTemplate validates the caller and template before delegating to the book.
func SaveTemplate(ctx context.Context, book TemplateBook, access Access, id string, in domain.Template) (string, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return "", ErrAccess
	}
	if id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return "", ErrTask
		}
	}
	normalized, _, err := domain.Normalize(in)
	if err != nil {
		return "", err
	}
	if _, err := uuid.Parse(strings.TrimSpace(normalized.AgentID)); err != nil {
		return "", domain.ErrAgent
	}
	return book.SaveTemplate(ctx, access, id, normalized)
}

// OpenTemplate validates the caller and template id before fetching the template.
func OpenTemplate(ctx context.Context, book TemplateBook, access Access, id string) (domain.Template, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return domain.Template{}, ErrAccess
	}
	if _, err := uuid.Parse(id); err != nil {
		return domain.Template{}, ErrTask
	}
	return book.OpenTemplate(ctx, access, id)
}

// RemoveTemplate validates the caller and template id before deleting the template.
func RemoveTemplate(ctx context.Context, book TemplateBook, access Access, id string) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(id); err != nil {
		return ErrTask
	}
	return book.RemoveTemplate(ctx, access, id)
}
