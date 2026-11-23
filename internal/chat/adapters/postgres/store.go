package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/tasks"
)

// Store is the Postgres writer for chats.
type Store struct {
	Pool     *pgxpool.Pool
	Enqueuer jobs.Enqueuer
	// ReplyRoute is the gateway route that answers a line when the person
	// names none. Empty turns replies off: a line is stored and nothing
	// answers it.
	ReplyRoute string
	// betweenReads, when set, runs inside Tail between its two reads. It is a
	// seam for tests of a read that races a write, set through export_test.go.
	betweenReads func()
}

// Open inserts one private chat. It does not touch a login session or the profile.
func (s Store) Open(ctx context.Context, access app.Access) (app.ChatSummary, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.ChatSummary{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.chats (owner_user_id, visibility)
		VALUES ($1::uuid, 'private')
		RETURNING id::text, created_at`, access.UserID).Scan(&id, &createdAt)
	if err != nil {
		return app.ChatSummary{}, fmt.Errorf("chat: open: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ChatCreate,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return app.ChatSummary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return app.ChatSummary{}, err
	}
	return app.ChatSummary{ID: id, Title: domain.NewChatTitle, LastActivity: createdAt.UTC()}, nil
}

// Say appends one line with the picks it was sent with and, in the same
// transaction, asks for its reply.
func (s Store) Say(ctx context.Context, access app.Access, chatID, body string, picks app.Picks) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockChat(ctx, tx, chatID); err != nil {
		return err
	}
	// A stale run is failed before the new line is stored, so its failed reply
	// keeps a lower seq than the line it held the chat for.
	if s.replying() {
		if err := failStaleRuns(ctx, tx, chatID); err != nil {
			return err
		}
	}
	var seq int
	var lineID string
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.lines (chat_id, seq, body, agent_id, route, pin_account_id, pin_model, author_id)
		SELECT $1::uuid,
		       COALESCE((SELECT MAX(seq) FROM chat.lines WHERE chat_id = $1::uuid), 0) + 1,
		       $2, $3::uuid, $4, $5::uuid, $6, $7::uuid
		RETURNING seq, id::text`, chatID, body,
		orNull(picks.AgentID), orNull(picks.Route), orNull(picks.PinAccount), orNull(picks.PinModel), access.UserID).Scan(&seq, &lineID)
	if err != nil {
		return writeErr(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ChatMessage,
		Outcome:  audit.Success,
		TargetID: lineID,
		Changes:  pickChanges(picks),
	}); err != nil {
		return err
	}
	if s.Enqueuer != nil && access.UserID != "" {
		if _, err := s.Enqueuer.InsertTx(ctx, tx, tasks.ScoutJobArgs{
			UserID:   access.UserID,
			Trigger:  "chat",
			SourceID: chatID,
		}, nil); err != nil {
			return fmt.Errorf("chat: scout enqueue: %w", err)
		}
		if _, err := s.Enqueuer.InsertTx(ctx, tx, memory.ExtractJobArgs{
			UserID:     access.UserID,
			SourceKind: "chat",
			ID:         chatID,
			Seq:        seq,
			Text:       body,
			AtUnix:     time.Now().UTC().Unix(),
		}, nil); err != nil {
			return fmt.Errorf("chat: extract enqueue: %w", err)
		}
	}
	if s.replying() {
		if err := s.reserveRun(ctx, tx, access, chatID, lineID, picks); err != nil {
			return err
		}
	} else if err := noReply(ctx, tx, chatID, lineID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// replying reports whether a line is answered by a model: replies are on and
// there is a queue to ask.
func (s Store) replying() bool { return s.ReplyRoute != "" && s.Enqueuer != nil }

// noReply stores the failed reply to a line nothing will answer, so that no
// line is left without one.
func noReply(ctx context.Context, tx pgx.Tx, chatID, lineID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, status, reply_to, attempts)
		SELECT $1::uuid, COALESCE(MAX(seq), 0) + 1, 'assistant', '', '', 'failed', $2::uuid,
		       '[{"model": "", "class": "no_replies"}]'::jsonb
		FROM chat.lines WHERE chat_id = $1::uuid`, chatID, lineID)
	if err != nil {
		return writeErr(err)
	}
	return nil
}

// failStaleRuns fails any run of the chat that outlived LiveFor, under the
// chat's lock already held. A run whose process died holds the chat for
// LiveFor; after that the next Say or Retry fails it so a fresh reply can be
// reserved.
func failStaleRuns(ctx context.Context, tx pgx.Tx, chatID string) error {
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM chat.runs
		WHERE chat_id = $1::uuid AND state = 'running'
		  AND created_at <= now() - make_interval(secs => $2::float8)
		FOR UPDATE`, chatID, app.LiveFor.Seconds())
	if err != nil {
		return fmt.Errorf("chat: stale runs: %w", err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("chat: stale runs: %w", err)
		}
		stale = append(stale, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("chat: stale runs: %w", err)
	}
	for _, id := range stale {
		if err := failRunTx(ctx, tx, chatID, id, app.ReplyEnd{Attempts: []domain.Attempt{{Model: "", Class: "timeout"}}}, false); err != nil {
			return err
		}
	}
	return nil
}

// reserveRun reserves the reply to the user line lineID, in the caller's
// transaction, with the picks it is to be answered with. It inserts the run
// row and queues round 1. A run already running in the chat is ErrBusy. Stale
// runs are failed first by the caller through failStaleRuns.
func (s Store) reserveRun(ctx context.Context, tx pgx.Tx, access app.Access, chatID, lineID string, picks app.Picks) error {
	replyID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.runs (id, chat_id, line_id, author_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)`,
		replyID, chatID, lineID, access.UserID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "runs_one_running" {
			return app.ErrBusy
		}
		return fmt.Errorf("chat: reserve run: %w", err)
	}

	route := picks.Route
	if route == "" {
		route = s.ReplyRoute
	}
	res, err := s.Enqueuer.InsertTx(ctx, tx, app.ReplyJobArgs{
		ChatID:      chatID,
		LineID:      lineID,
		ReplyID:     replyID.String(),
		Round:       1,
		UserID:      access.UserID,
		Route:       route,
		RoutePicked: picks.Route != "",
		PinAccount:  picks.PinAccount,
		PinModel:    picks.PinModel,
		AgentID:     picks.AgentID,
	}, nil)
	if err != nil {
		return fmt.Errorf("chat: reply enqueue: %w", err)
	}
	if res.UniqueSkippedAsDuplicate {
		return fmt.Errorf("chat: reply was not queued")
	}
	return nil
}

// orNull binds an empty pick as NULL.
func orNull(pick string) *string {
	if pick == "" {
		return nil
	}
	return &pick
}

// pickChanges lists a line's picks as the safe values the audit catalog
// admits: the route and the ids and names of the model and the agent. The
// line's text is content and never appears.
func pickChanges(picks app.Picks) audit.Changes {
	return audit.Changes{
		Fields: []string{"agent_id", "model_id", "model_provider", "route"},
		Values: map[string]audit.Change{
			"route":          {To: picks.Route},
			"model_provider": {To: picks.PinAccount},
			"model_id":       {To: picks.PinModel},
			"agent_id":       {To: picks.AgentID},
		},
	}
}

// goneOrSay splits a locked miss in Say. The lock asks for edit rights too, so
// a chat this person can only view hides inside it: one unlocked look tells a
// read-only chat from one that is gone.
func goneOrSay(ctx context.Context, tx pgx.Tx, chatID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&one)
	switch {
	case err == nil:
		return app.ErrSay
	case errors.Is(err, pgx.ErrNoRows):
		return app.ErrGone
	default:
		return fmt.Errorf("chat: view: %w", err)
	}
}

// List returns the chats this person can view under row security, newest
// activity first, at most 100.
func (s Store) List(ctx context.Context, access app.Access) ([]app.ChatSummary, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT chat.id::text,
		       COALESCE((
		         SELECT body FROM chat.lines AS line
		         WHERE line.chat_id = chat.id AND line.role = 'user'
		         ORDER BY line.seq
		         LIMIT 1
		       ), '') AS first_user_line,
		       COALESCE((
		         SELECT body FROM chat.lines AS line
		         WHERE line.chat_id = chat.id
		         ORDER BY line.seq DESC
		         LIMIT 1
		       ), '') AS newest_line,
		       COALESCE((
		         SELECT line.at FROM chat.lines AS line
		         WHERE line.chat_id = chat.id
		         ORDER BY line.seq DESC
		         LIMIT 1
		       ), chat.created_at) AS last_activity
		FROM chat.chats AS chat
		ORDER BY last_activity DESC, chat.id DESC
		LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("chat: list: %w", err)
	}
	defer rows.Close()

	out := []app.ChatSummary{}
	for rows.Next() {
		var summary app.ChatSummary
		var firstUser, newest string
		if err := rows.Scan(&summary.ID, &firstUser, &newest, &summary.LastActivity); err != nil {
			return nil, fmt.Errorf("chat: list: %w", err)
		}
		summary.Title = domain.Title(firstUser)
		summary.Preview = domain.Preview(newest)
		summary.LastActivity = summary.LastActivity.UTC()
		out = append(out, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: list: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// Read returns the lines in order.
func (s Store) Read(ctx context.Context, access app.Access, chatID string) (app.Chat, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Chat{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tainted, privateRead bool
	err = tx.QueryRow(ctx, `SELECT tainted, private_read FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&tainted, &privateRead)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Chat{}, app.ErrGone
	}
	if err != nil {
		return app.Chat{}, fmt.Errorf("chat: read: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT `+lineColumnsFor("$2::uuid")+`
		FROM chat.lines AS line
		WHERE chat_id = $1::uuid
		  AND NOT (`+superseded+`)
		ORDER BY seq`, chatID, access.UserID)
	if err != nil {
		return app.Chat{}, fmt.Errorf("chat: lines: %w", err)
	}
	defer rows.Close()
	lines := make([]app.Line, 0)
	for rows.Next() {
		line, err := scanLine(rows)
		if err != nil {
			return app.Chat{}, err
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return app.Chat{}, fmt.Errorf("chat: lines: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Chat{}, err
	}
	return app.Chat{ID: chatID, Lines: lines, Tainted: tainted, PrivateRead: privateRead}, nil
}

func (s Store) begin(ctx context.Context, access app.Access) (pgx.Tx, error) {
	return s.beginWith(ctx, access, pgx.TxOptions{})
}

func (s Store) beginWith(ctx context.Context, access app.Access, opts pgx.TxOptions) (pgx.Tx, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("chat: no database")
	}
	tx, err := s.Pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', $3, true),
		       set_config('upmo.shared_view', $4, true),
		       set_config('upmo.shared_edit', $5, true)`,
		accessArgs(access)...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// accessArgs are the begin settings. An empty team or share list is "{}".
func accessArgs(access app.Access) []any {
	return []any{access.UserID, access.Allow, arrayOr(access.Teams), arrayOr(access.SharedView), arrayOr(access.SharedEdit)}
}

func arrayOr(literal string) string {
	if literal == "" {
		return "{}"
	}
	return literal
}

func writeErr(err error) error {
	if strings.Contains(err.Error(), "chat: writer") {
		return domain.ErrWriter
	}
	return fmt.Errorf("chat: line: %w", err)
}

// lineColumnsFor are the columns scanLine reads, from a chat.lines row named
// line. viewer is an SQL uuid expression for the person reading: only the
// author of a line gets the account and agent they picked back, anyone else
// gets the route and the model name alone.
func lineColumnsFor(viewer string) string {
	return `line.id::text, line.role, line.body, line.model, line.tokens, line.cost_micro_usd, line.at,
	line.status, line.reply_to::text, line.attempts,
	line.route,
	CASE WHEN line.author_id = ` + viewer + ` THEN line.pin_account_id::text END,
	line.pin_model,
	CASE WHEN line.author_id = ` + viewer + ` THEN line.agent_id::text END,
	(line.role = 'user' AND NOT EXISTS (SELECT 1 FROM chat.lines AS reply WHERE reply.reply_to = line.id))`
}

// superseded holds for a failed reply that a later reply to the same user
// line has answered, on a chat.lines row named line. Such a line stays
// stored but is hidden from the thread and from the history.
const superseded = `line.status = 'failed' AND EXISTS (
	SELECT 1 FROM chat.lines AS later
	WHERE later.reply_to = line.reply_to AND later.status = 'done' AND later.seq > line.seq
)`

// scanLine reads the lineColumnsFor of a row, after any lead columns.
func scanLine(rows pgx.Rows, lead ...any) (app.Line, error) {
	var line app.Line
	var attempts []byte
	dest := append(lead, &line.ID, &line.Role, &line.Body, &line.Model, &line.Tokens, &line.CostMicroUSD, &line.At,
		&line.Status, &line.ReplyTo, &attempts, &line.Route, &line.PinAccount, &line.PinModel, &line.AgentID, &line.Pending)
	if err := rows.Scan(dest...); err != nil {
		return app.Line{}, fmt.Errorf("chat: line: %w", err)
	}
	if attempts != nil {
		if err := json.Unmarshal(attempts, &line.Attempts); err != nil {
			return app.Line{}, fmt.Errorf("chat: line attempts: %w", err)
		}
	}
	return line, nil
}
