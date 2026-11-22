package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/platform/stream"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Channel is the NOTIFY channel the task event triggers send on.
const Channel = "task_stream"

// Tail returns what a task gained after the event id after ("" for from the
// start): its events and chat lines in id order, at most limit, mapped as the
// detail read model maps them, and the task's status. It runs in one
// transaction under row security. A task this person cannot see is ErrGone.
func (s Store) Tail(ctx context.Context, access app.Access, taskID, after string, limit int) (app.Tail, error) {
	// One snapshot for every read: under read committed a step that commits
	// between the status read and the events read gave its last event with
	// the old status, and the stream never showed Done.
	tx, err := s.beginWith(ctx, access, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return app.Tail{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM tasks.tasks WHERE id = $1::uuid`, taskID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Tail{}, app.ErrGone
	}
	if err != nil {
		return app.Tail{}, fmt.Errorf("tasks: tail: %w", err)
	}

	// The phase before the first event of the batch, so a chat line shows the
	// phase it was written in.
	var seed domain.Status
	if after != "" {
		seed, err = lastPhase(ctx, tx, taskID, after)
		if err != nil {
			return app.Tail{}, err
		}
	}

	entries, err := readEntriesAfter(ctx, tx, taskID, after, limit+1)
	if err != nil {
		return app.Tail{}, err
	}
	var tail app.Tail
	if len(entries) > limit {
		entries = entries[:limit]
		tail.More = true
	}
	events, phase, err := shape(entries, seed)
	if err != nil {
		return app.Tail{}, err
	}
	if tail.More {
		phase, err = lastPhase(ctx, tx, taskID, "")
		if err != nil {
			return app.Tail{}, err
		}
	}
	tail.Events = events
	tail.State = domain.Status(status)
	tail.Status, err = domain.Show(tail.State)
	if err != nil {
		return app.Tail{}, fmt.Errorf("tasks: status %q: %w", status, err)
	}
	tail.Phase = domain.PhaseOf(tail.State, phase)
	if err := tx.Commit(ctx); err != nil {
		return app.Tail{}, err
	}
	return tail, nil
}

// lastPhase returns the last phase a task's transitions reached up to the
// event id upTo, or to the end when upTo is "". It is "" before the first.
func lastPhase(ctx context.Context, tx pgx.Tx, taskID, upTo string) (domain.Status, error) {
	var phase string
	err := tx.QueryRow(ctx, `
		SELECT to_status
		FROM tasks.task_events
		WHERE task_id = $1::uuid
		  AND ($2::text = '' OR id <= NULLIF($2::text, '')::uuid)
		  AND to_status IN ('Preparing', 'Planning', 'Working', 'Checking')
		ORDER BY seq DESC
		LIMIT 1`, taskID, upTo).Scan(&phase)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("tasks: tail phase: %w", err)
	}
	return domain.Status(phase), nil
}

// readEntriesAfter returns the rows of both event tables with an id after
// the cursor in id order, at most limit of them. Ids are time ordered across
// the two tables, so the first limit of the union are within the first limit
// of each.
func readEntriesAfter(ctx context.Context, tx pgx.Tx, taskID, after string, limit int) ([]entry, error) {
	var entries []entry
	rows, err := tx.Query(ctx, `
		SELECT id::text, at, input, to_status
		FROM tasks.task_events
		WHERE task_id = $1::uuid AND ($2::text = '' OR id > NULLIF($2::text, '')::uuid)
		ORDER BY id
		LIMIT $3`, taskID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("tasks: tail events: %w", err)
	}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.at, &e.input, &e.to); err != nil {
			rows.Close()
			return nil, fmt.Errorf("tasks: tail events: %w", err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: tail events: %w", err)
	}

	rows, err = tx.Query(ctx, `
		SELECT id::text, at, body
		FROM tasks.chat_events
		WHERE task_id = $1::uuid AND ($2::text = '' OR id > NULLIF($2::text, '')::uuid)
		ORDER BY id
		LIMIT $3`, taskID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("tasks: tail chat events: %w", err)
	}
	for rows.Next() {
		e := entry{isChat: true}
		if err := rows.Scan(&e.id, &e.at, &e.body); err != nil {
			rows.Close()
			return nil, fmt.Errorf("tasks: tail chat events: %w", err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: tail chat events: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// Listener holds one connection of its own, outside any pool, that LISTENs on
// the task event channel. It reconnects with a growing pause when the
// connection drops.
type Listener struct {
	// Config is how to connect. The connection is not pooled and runs no role
	// statement: LISTEN needs no privilege.
	Config *pgx.ConnConfig
	Log    *slog.Logger
	// MinBackoff and MaxBackoff bound the pause before a reconnect. Zero
	// takes 200 ms and 10 s.
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// ApplicationName names the listener connection in pg_stat_activity.
const ApplicationName = "upmo-task-stream"

// Listen implements app.StreamFeed.
func (l Listener) Listen(ctx context.Context, notify func(key string), resync func()) {
	stream.Listener{
		Config:          l.Config,
		Log:             l.Log,
		Channel:         Channel,
		ApplicationName: ApplicationName,
		Subject:         "task",
		MinBackoff:      l.MinBackoff,
		MaxBackoff:      l.MaxBackoff,
	}.Listen(ctx, notify, resync)
}
