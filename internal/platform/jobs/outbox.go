package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ErrOutboxTopic is returned when the topic is blank.
var ErrOutboxTopic = errors.New("jobs: outbox topic is required")

// ErrOutboxPayload is returned when the payload is empty or not JSON.
var ErrOutboxPayload = errors.New("jobs: outbox payload must be non-empty JSON")

// OutboxInsert writes one outbox row in the same transaction as the caller's
// durable write. The row is visible only after commit.
func OutboxInsert(ctx context.Context, tx pgx.Tx, topic string, payload []byte) error {
	if strings.TrimSpace(topic) == "" {
		return ErrOutboxTopic
	}
	if len(payload) == 0 || !json.Valid(payload) {
		return ErrOutboxPayload
	}
	_, err := tx.Exec(ctx, `
INSERT INTO platform.outbox (topic, payload)
VALUES ($1, $2::jsonb)
`, topic, payload)
	if err != nil {
		return fmt.Errorf("jobs: outbox insert: %w", err)
	}
	return nil
}
