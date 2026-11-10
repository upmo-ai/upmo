package postgres

import (
	"context"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// RecentLines returns the bodies of the last limit chat lines the access may
// read, oldest of them first.
func (s Store) RecentLines(ctx context.Context, access app.Access, chatID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1
	}
	chat, err := s.Read(ctx, access, chatID)
	if err != nil {
		return nil, err
	}
	bodies := make([]string, 0, len(chat.Lines))
	for _, line := range chat.Lines {
		if line.Status == domain.StatusFailed {
			continue
		}
		bodies = append(bodies, line.Body)
	}
	if len(bodies) > limit {
		bodies = bodies[len(bodies)-limit:]
	}
	return bodies, nil
}
