package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/chat/app"
)

// RecentLines returns the bodies of the last limit chat lines for one person,
// oldest of them first.
func (s Store) RecentLines(ctx context.Context, userID uuid.UUID, chatID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1
	}
	access := app.Access{UserID: userID.String(), Allow: "chat:view"}
	chat, err := s.Read(ctx, access, chatID)
	if err != nil {
		return nil, err
	}
	bodies := make([]string, 0, len(chat.Lines))
	for _, line := range chat.Lines {
		bodies = append(bodies, line.Body)
	}
	if len(bodies) > limit {
		bodies = bodies[len(bodies)-limit:]
	}
	return bodies, nil
}
