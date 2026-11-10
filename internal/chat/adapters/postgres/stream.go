package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/chat/app"
)

// Channel is the NOTIFY channel the delta and line triggers send on.
const Channel = "chat_stream"

// Newest returns the stream id of the newest line of a chat, "" for a chat
// with none. It is also the access check of a stream: a chat this person
// cannot see is ErrGone.
func (s Store) Newest(ctx context.Context, access app.Access, chatID string) (string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := visible(ctx, tx, chatID); err != nil {
		return "", err
	}
	// The client has the lines stored now. A reply still under way is seen
	// from its start, so the stream begins at the newest line older than its
	// earliest delta; lines newer than that are replayed and the client drops
	// the ones it has.
	var newest string
	var first *string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM chat.deltas WHERE chat_id = $1::uuid ORDER BY id LIMIT 1`, chatID).Scan(&first)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("chat: newest: %w", err)
	}
	err = tx.QueryRow(ctx, `
		SELECT feed_id::text FROM chat.lines
		WHERE chat_id = $1::uuid AND ($2::text IS NULL OR feed_id < $2::uuid)
		ORDER BY feed_id DESC LIMIT 1`, chatID, first).Scan(&newest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("chat: newest: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return newest, nil
}

func visible(ctx context.Context, tx pgx.Tx, chatID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("chat: view: %w", err)
	}
	return nil
}

// Tail returns what a chat gained after the stream id after ("" for from the
// start): its deltas and lines in id order, at most limit. It runs in one
// transaction under row security. A line's id is its feed id, made when the
// line was stored and so after every delta of that line.
func (s Store) Tail(ctx context.Context, access app.Access, chatID, after string, limit int) (app.Tail, error) {
	// One snapshot for both reads: a reply's deltas and its finished line are
	// never in it together, since the line is stored as its deltas are deleted.
	tx, err := s.beginWith(ctx, access, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return app.Tail{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := visible(ctx, tx, chatID); err != nil {
		return app.Tail{}, err
	}

	// Ids are time ordered across the two tables, so the first limit+1 of
	// the union are within the first limit+1 of each.
	var events []app.StreamEvent
	rows, err := tx.Query(ctx, `
		SELECT id::text, line_id::text, seq, kind, body
		FROM chat.deltas
		WHERE chat_id = $1::uuid AND ($2::text = '' OR id > NULLIF($2::text, '')::uuid)
		ORDER BY id
		LIMIT $3`, chatID, after, limit+1)
	if err != nil {
		return app.Tail{}, fmt.Errorf("chat: tail deltas: %w", err)
	}
	for rows.Next() {
		var event app.StreamEvent
		var kind string
		if err := rows.Scan(&event.ID, &event.LineID, &event.Seq, &kind, &event.Body); err != nil {
			rows.Close()
			return app.Tail{}, fmt.Errorf("chat: tail deltas: %w", err)
		}
		event.Kind = app.EventDelta
		if kind == "status" {
			event.Kind = app.EventStatus
			if err := json.Unmarshal([]byte(event.Body), &event.Notice); err != nil {
				rows.Close()
				return app.Tail{}, fmt.Errorf("chat: tail notice: %w", err)
			}
			event.Body = ""
		}
		events = append(events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return app.Tail{}, fmt.Errorf("chat: tail deltas: %w", err)
	}

	if s.BetweenReads != nil {
		s.BetweenReads()
	}
	rows, err = tx.Query(ctx, `
		SELECT line.feed_id::text, `+lineColumnsFor("$4::uuid")+`
		FROM chat.lines AS line
		WHERE line.chat_id = $1::uuid AND ($2::text = '' OR line.feed_id > NULLIF($2::text, '')::uuid)
		ORDER BY line.feed_id
		LIMIT $3`, chatID, after, limit+1, access.UserID)
	if err != nil {
		return app.Tail{}, fmt.Errorf("chat: tail lines: %w", err)
	}
	for rows.Next() {
		var feedID string
		line, err := scanLine(rows, &feedID)
		if err != nil {
			rows.Close()
			return app.Tail{}, err
		}
		events = append(events, app.StreamEvent{ID: feedID, Kind: app.EventLine, LineID: line.ID, Line: line})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return app.Tail{}, fmt.Errorf("chat: tail lines: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Tail{}, err
	}

	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
	var tail app.Tail
	if len(events) > limit {
		events = events[:limit]
		tail.More = true
	}
	tail.Events = events
	return tail, nil
}

// Listener holds one connection of its own, outside any pool, that LISTENs on
// the chat stream channel. It reconnects with a growing pause when the
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
const ApplicationName = "upmo-chat-stream"

// keepalive is how long the listener waits for a notification before it
// checks that the connection still answers.
const keepalive = 30 * time.Second

// Listen implements app.StreamFeed.
func (l Listener) Listen(ctx context.Context, notify func(chatID string), resync func()) {
	minPause, maxPause := l.MinBackoff, l.MaxBackoff
	if minPause <= 0 {
		minPause = 200 * time.Millisecond
	}
	if maxPause <= 0 {
		maxPause = 10 * time.Second
	}
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	pause := minPause
	for ctx.Err() == nil {
		started := time.Now()
		err := l.listenOnce(ctx, notify, resync)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > maxPause {
			pause = minPause
		}
		log.Warn("chat stream listener lost its connection", "err", err, "retry_in", pause.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(pause*2, maxPause)
	}
}

func (l Listener) listenOnce(ctx context.Context, notify func(string), resync func()) error {
	cfg := l.Config.Copy()
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = ApplicationName
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	// Listening has begun, so a notification from here on arrives. Rows
	// written before it are read from the tables.
	resync()
	for {
		wait, cancel := context.WithTimeout(ctx, keepalive)
		note, err := conn.WaitForNotification(wait)
		cancel()
		switch {
		case err == nil:
			var payload struct {
				Chat string `json:"chat"`
			}
			if json.Unmarshal([]byte(note.Payload), &payload) == nil && payload.Chat != "" {
				notify(payload.Chat)
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			pctx, pcancel := context.WithTimeout(ctx, keepalive)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return err
			}
		default:
			return err
		}
	}
}
