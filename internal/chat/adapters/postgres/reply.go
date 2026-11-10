package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// lockChat locks the chat row for a write and tells a chat that is gone from
// one this person can only view. Two replies of one chat start one at a time
// under this lock.
func lockChat(ctx context.Context, tx pgx.Tx, chatID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM chat.chats WHERE id = $1::uuid FOR UPDATE`, chatID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return goneOrSay(ctx, tx, chatID)
	}
	if err != nil {
		return fmt.Errorf("chat: lock: %w", err)
	}
	return nil
}

// Begin looks at the reply to a user line. It runs as the person who wrote
// the line, under row security, and holds the chat's lock while it decides:
//
//   - a line stored under the reply id means the reply is done;
//   - deltas under it mean an earlier run died, and are returned as a partial;
//   - a running reply of the chat, or an earlier user line that has no reply
//     yet, means wait; both stop counting after app.LiveFor;
//   - otherwise the reply is marked as started with an empty text delta of
//     seq 0, and the lines to send come back.
func (s Store) Begin(ctx context.Context, access app.Access, job app.ReplyJobArgs) (app.ReplyTurn, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.ReplyTurn{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChat(ctx, tx, job.ChatID); err != nil {
		return app.ReplyTurn{}, err
	}

	var stored bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat.lines WHERE id = $1::uuid)`, job.ReplyID).Scan(&stored); err != nil {
		return app.ReplyTurn{}, fmt.Errorf("chat: reply: %w", err)
	}
	if stored {
		return app.ReplyTurn{State: app.ReplyDone}, nil
	}

	var userSeq int
	err = tx.QueryRow(ctx, `
		SELECT seq FROM chat.lines
		WHERE id = $1::uuid AND chat_id = $2::uuid AND role = 'user'`, job.LineID, job.ChatID).Scan(&userSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ReplyTurn{}, app.ErrNoLine
	}
	if err != nil {
		return app.ReplyTurn{}, fmt.Errorf("chat: reply line: %w", err)
	}

	left, err := leftBehind(ctx, tx, job.ChatID, job.ReplyID)
	if err != nil {
		return app.ReplyTurn{}, err
	}
	if left != nil {
		return *left, nil
	}

	var busy bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		         SELECT 1 FROM chat.deltas
		         WHERE chat_id = $1::uuid AND line_id <> $2::uuid
		           AND at > now() - make_interval(secs => $4::float8))
		    OR EXISTS (
		         SELECT 1 FROM chat.lines AS earlier
		         WHERE earlier.chat_id = $1::uuid AND earlier.role = 'user' AND earlier.seq < $3
		           AND earlier.at > now() - make_interval(secs => $4::float8)
		           AND NOT EXISTS (SELECT 1 FROM chat.lines AS reply WHERE reply.reply_to = earlier.id))`,
		job.ChatID, job.ReplyID, userSeq, app.LiveFor.Seconds()).Scan(&busy)
	if err != nil {
		return app.ReplyTurn{}, fmt.Errorf("chat: reply busy: %w", err)
	}
	if busy {
		return app.ReplyTurn{State: app.ReplyWait}, nil
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.deltas (chat_id, line_id, seq, kind, body)
		VALUES ($1::uuid, $2::uuid, 0, 'text', '')`, job.ChatID, job.ReplyID); err != nil {
		return app.ReplyTurn{}, fmt.Errorf("chat: reply start: %w", err)
	}

	// Every earlier exchange, each answer right after the user line it
	// answers: an answer written after a follow-up was sent still belongs to
	// the line before it. Failed and replaced replies are left out, and so is
	// whatever answers a line after this one.
	rows, err := tx.Query(ctx, `
		SELECT `+lineColumns+`
		FROM chat.lines AS line
		LEFT JOIN chat.lines AS asked ON asked.id = line.reply_to
		WHERE line.chat_id = $1::uuid
		  AND COALESCE(asked.seq, line.seq) <= $2
		  AND NOT (line.status = 'failed' OR EXISTS (
		        SELECT 1 FROM chat.lines AS later
		        WHERE later.reply_to = line.reply_to AND later.status = 'done' AND later.seq > line.seq))
		ORDER BY COALESCE(asked.seq, line.seq), (line.role = 'assistant'), line.seq`, job.ChatID, userSeq)
	if err != nil {
		return app.ReplyTurn{}, fmt.Errorf("chat: reply history: %w", err)
	}
	defer rows.Close()
	var lines []app.Line
	for rows.Next() {
		line, err := scanLine(rows)
		if err != nil {
			return app.ReplyTurn{}, err
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return app.ReplyTurn{}, fmt.Errorf("chat: reply history: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return app.ReplyTurn{}, err
	}
	return app.ReplyTurn{State: app.ReplyGo, Lines: lines}, nil
}

// leftBehind reads the deltas an earlier run of this reply wrote. It is nil
// when there are none.
func leftBehind(ctx context.Context, tx pgx.Tx, chatID, replyID string) (*app.ReplyTurn, error) {
	rows, err := tx.Query(ctx, `
		SELECT kind, body FROM chat.deltas
		WHERE chat_id = $1::uuid AND line_id = $2::uuid ORDER BY seq`, chatID, replyID)
	if err != nil {
		return nil, fmt.Errorf("chat: reply deltas: %w", err)
	}
	defer rows.Close()
	turn := app.ReplyTurn{State: app.ReplyRescue}
	found := false
	for rows.Next() {
		var kind, body string
		if err := rows.Scan(&kind, &body); err != nil {
			return nil, fmt.Errorf("chat: reply deltas: %w", err)
		}
		found = true
		if kind == "text" {
			turn.Partial += body
			continue
		}
		var notice app.Notice
		if err := json.Unmarshal([]byte(body), &notice); err != nil {
			return nil, fmt.Errorf("chat: reply notice: %w", err)
		}
		turn.Attempts = append(turn.Attempts, domain.Attempt{Model: notice.FailedModel, Class: notice.Class})
		turn.Model = notice.NextModel
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: reply deltas: %w", err)
	}
	if !found {
		return nil, nil
	}
	return &turn, nil
}

// PutDelta stores one row of a reply that is streaming, in a transaction of
// its own so that it commits, and is announced, at once.
func (s Store) PutDelta(ctx context.Context, access app.Access, chatID, replyID string, seq int, kind, body string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.deltas (chat_id, line_id, seq, kind, body)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, chatID, replyID, seq, kind, body); err != nil {
		return fmt.Errorf("chat: delta: %w", err)
	}
	return tx.Commit(ctx)
}

// Finish stores the assistant line and deletes the deltas of the reply in one
// transaction, so a reader sees the deltas or the line and never both.
func (s Store) Finish(ctx context.Context, access app.Access, reply app.Reply) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChat(ctx, tx, reply.ChatID); err != nil {
		return err
	}
	var attempts []byte
	if reply.Status == domain.StatusFailed {
		attempts, err = json.Marshal(append([]domain.Attempt{}, reply.Attempts...))
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.lines
		  (id, chat_id, seq, role, body, model, tokens, cost_micro_usd, status, reply_to, attempts)
		SELECT $1::uuid, $2::uuid, COALESCE(MAX(seq), 0) + 1, 'assistant', $3, $4, $5, $6, $7, $8::uuid, $9::jsonb
		FROM chat.lines WHERE chat_id = $2::uuid`,
		reply.LineID, reply.ChatID, reply.Body, reply.Model, reply.Tokens, reply.CostMicroUSD,
		reply.Status, reply.ReplyTo, attempts); err != nil {
		return writeErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chat.deltas WHERE chat_id = $1::uuid AND line_id = $2::uuid`, reply.ChatID, reply.LineID); err != nil {
		return fmt.Errorf("chat: deltas: %w", err)
	}
	return tx.Commit(ctx)
}

// Sweep deletes the deltas that are older than two hours.
func (s Store) Sweep(ctx context.Context) (int64, error) {
	if s.Pool == nil {
		return 0, fmt.Errorf("chat: no database")
	}
	var swept int64
	if err := s.Pool.QueryRow(ctx, `SELECT chat.sweep_deltas()`).Scan(&swept); err != nil {
		return 0, fmt.Errorf("chat: sweep: %w", err)
	}
	return swept, nil
}

// FailReply stores the failed reply to a user line for a person who can no
// longer write it, or for a job that could not start, and deletes the reply's
// deltas. why is access or unwritten. It reports false when there is nothing
// to store: no such user line, or the line is already answered.
func (s Store) FailReply(ctx context.Context, userLine, replyID, why string) (bool, error) {
	if s.Pool == nil {
		return false, fmt.Errorf("chat: no database")
	}
	var outcome string
	if err := s.Pool.QueryRow(ctx, `SELECT chat.fail_reply($1::uuid, $2::uuid, $3)`, userLine, replyID, why).Scan(&outcome); err != nil {
		return false, fmt.Errorf("chat: fail reply: %w", err)
	}
	switch outcome {
	case "stored":
		return true, nil
	case "answered":
		return false, nil
	}
	return false, fmt.Errorf("chat: fail reply: no user line with a reply job for it")
}

// CanWrite reports ErrGone or ErrSay when this person can no longer write to
// the chat, under the access given.
func (s Store) CanWrite(ctx context.Context, access app.Access, chatID string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return lockChat(ctx, tx, chatID)
}

// Retry asks again for the reply to the user line that a failed assistant line
// left unanswered. The failed line stays; it is hidden once a later reply to
// the same user line succeeds.
func (s Store) Retry(ctx context.Context, access app.Access, chatID, lineID, route string) error {
	if s.Enqueuer == nil || s.ReplyRoute == "" {
		return app.ErrReplies
	}
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChat(ctx, tx, chatID); err != nil {
		return err
	}
	var role, status string
	var userLine *string
	err = tx.QueryRow(ctx, `
		SELECT role, status, reply_to::text FROM chat.lines
		WHERE id = $1::uuid AND chat_id = $2::uuid`, lineID, chatID).Scan(&role, &status, &userLine)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNoLine
	}
	if err != nil {
		return fmt.Errorf("chat: retry line: %w", err)
	}
	if role != domain.RoleAssistant || status != domain.StatusFailed || userLine == nil {
		return app.ErrRetry
	}
	var answered bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM chat.lines WHERE reply_to = $1::uuid AND status = 'done')`, *userLine).Scan(&answered); err != nil {
		return fmt.Errorf("chat: retry answered: %w", err)
	}
	if answered {
		return app.ErrRetry
	}
	queued, err := s.enqueueReply(ctx, tx, access, chatID, *userLine, route)
	if err != nil {
		return err
	}
	if !queued {
		return app.ErrBusy
	}
	return tx.Commit(ctx)
}
