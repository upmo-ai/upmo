package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrOutboxTopic is returned when the topic is blank.
var ErrOutboxTopic = errors.New("jobs: outbox topic is required")

// ErrOutboxPayload is returned when the payload is empty or not JSON.
var ErrOutboxPayload = errors.New("jobs: outbox payload must be non-empty JSON")

// ErrOutboxUnknownTopic is returned when DrainOnce sees a topic with no handler.
var ErrOutboxUnknownTopic = errors.New("jobs: outbox topic has no handler")

// Handler delivers one outbox payload for a topic. It must be idempotent.
type Handler func(ctx context.Context, topic string, payload []byte) error

var (
	handlersMu sync.RWMutex
	handlers   = map[string]Handler{}
)

// Register sets the handler for topic. An empty topic is ignored. Pass nil to
// clear a topic (tests).
func Register(topic string, h Handler) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return
	}
	handlersMu.Lock()
	defer handlersMu.Unlock()
	if h == nil {
		delete(handlers, topic)
		return
	}
	handlers[topic] = h
}

// ClearHandlers removes every registered outbox handler (tests).
func ClearHandlers() {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	handlers = map[string]Handler{}
}

func handlerFor(topic string) Handler {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	return handlers[topic]
}

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

const defaultDrainLimit = 32

// DrainOnce claims up to limit unprocessed outbox rows, dispatches each to its
// topic handler, and sets processed_at only on success. Unknown topics and
// handler failures leave processed_at null. Returns how many rows were marked
// processed, and a non-nil error when any row was unknown or failed.
func DrainOnce(ctx context.Context, pool *pgxpool.Pool, limit int) (int, error) {
	if pool == nil {
		return 0, fmt.Errorf("jobs: outbox drain: no pool")
	}
	if limit <= 0 {
		limit = defaultDrainLimit
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
SELECT id, topic, payload
FROM platform.outbox
WHERE processed_at IS NULL
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT $1
`, limit)
	if err != nil {
		return 0, fmt.Errorf("jobs: outbox claim: %w", err)
	}
	type claimed struct {
		id      uuid.UUID
		topic   string
		payload []byte
	}
	var batch []claimed
	for rows.Next() {
		var row claimed
		if err := rows.Scan(&row.id, &row.topic, &row.payload); err != nil {
			rows.Close()
			return 0, fmt.Errorf("jobs: outbox scan: %w", err)
		}
		batch = append(batch, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	processed := 0
	var firstErr error
	noteErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, row := range batch {
		h := handlerFor(row.topic)
		if h == nil {
			slog.ErrorContext(ctx, "outbox topic has no handler", "topic", row.topic, "id", row.id)
			noteErr(fmt.Errorf("%w: %s", ErrOutboxUnknownTopic, row.topic))
			continue
		}
		if err := h(ctx, row.topic, row.payload); err != nil {
			slog.ErrorContext(ctx, "outbox handler failed", "topic", row.topic, "id", row.id, "err", err)
			noteErr(err)
			continue
		}
		if _, err := tx.Exec(ctx, `
UPDATE platform.outbox
SET processed_at = now()
WHERE id = $1 AND processed_at IS NULL
`, row.id); err != nil {
			return processed, fmt.Errorf("jobs: outbox mark: %w", err)
		}
		processed++
	}
	if err := tx.Commit(ctx); err != nil {
		return processed, err
	}
	return processed, firstErr
}
