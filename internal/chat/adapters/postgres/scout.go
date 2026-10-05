package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/chat/app"
)

// RecentLines returns the last limit chat lines for one person.
func (s Store) RecentLines(ctx context.Context, userID uuid.UUID, chatID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1
	}
	access := app.Access{UserID: userID.String(), Allow: "chat:view"}
	chat, err := s.Read(ctx, access, chatID)
	if err != nil {
		return nil, err
	}
	lines := chat.Lines
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}
