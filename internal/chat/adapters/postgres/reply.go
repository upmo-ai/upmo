package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// lockChat locks the chat row for a write and tells a chat that is gone from
// one this person can only view. The lock is FOR NO KEY UPDATE, so it never
// blocks the key share the foreign keys of deltas, lines and turns take on the
// chat row. Two replies of one chat are reserved one at a time under this lock.
func lockChat(ctx context.Context, tx pgx.Tx, chatID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM chat.chats WHERE id = $1::uuid FOR NO KEY UPDATE`, chatID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return goneOrSay(ctx, tx, chatID)
	}
	if err != nil {
		return fmt.Errorf("chat: lock: %w", err)
	}
	return nil
}

// historyLinesTx loads the transcript a round-1 reply sends: every earlier
// exchange, each answer right after the user line it answers, without failed
// or replaced replies. ErrNoLine when the user line is not there.
func historyLinesTx(ctx context.Context, tx pgx.Tx, chatID, lineID string) ([]app.Line, error) {
	var userSeq int
	err := tx.QueryRow(ctx, `
		SELECT seq FROM chat.lines
		WHERE id = $1::uuid AND chat_id = $2::uuid AND role = 'user'`, lineID, chatID).Scan(&userSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrNoLine
	}
	if err != nil {
		return nil, fmt.Errorf("chat: reply line: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT `+lineColumnsFor("NULL::uuid")+`
		FROM chat.lines AS line
		LEFT JOIN chat.lines AS asked ON asked.id = line.reply_to
		WHERE line.chat_id = $1::uuid
		  AND COALESCE(asked.seq, line.seq) <= $2
		  AND NOT (line.status IN ('failed', 'stopped') OR EXISTS (
		        SELECT 1 FROM chat.lines AS later
		        WHERE later.reply_to = line.reply_to AND later.status = 'done' AND later.seq > line.seq))
		ORDER BY COALESCE(asked.seq, line.seq), (line.role = 'assistant'), line.seq`, chatID, userSeq)
	if err != nil {
		return nil, fmt.Errorf("chat: reply history: %w", err)
	}
	defer rows.Close()
	var lines []app.Line
	for rows.Next() {
		line, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: reply history: %w", err)
	}
	return lines, nil
}

// Claim takes the run's current round under the chat's lock and hands the
// worker the round: the messages to send and the state the earlier rounds
// left. ErrGone and ErrSay mean the person can no longer write to the chat;
// ErrNoLine that the run or its line is not there.
func (s Store) Claim(ctx context.Context, access app.Access, job app.ReplyJobArgs, token string) (app.Claim, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Claim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChat(ctx, tx, job.ChatID); err != nil {
		return app.Claim{}, err
	}
	var vis, team string
	var private bool
	var exposure agentloop.ExposureState
	if err := tx.QueryRow(ctx, `SELECT visibility, COALESCE(visibility_team_id::text, ''), tainted, private_read, visibility = 'private' AND NOT EXISTS (SELECT 1 FROM identity.shares WHERE object_type = 'chat' AND object_id = chat.chats.id) FROM chat.chats WHERE id = $1::uuid`, job.ChatID).Scan(&vis, &team, &exposure.Tainted, &exposure.PrivateRead, &private); err != nil {
		return app.Claim{}, fmt.Errorf("chat: claim scope: %w", err)
	}
	claim := app.Claim{
		Job:      job,
		Token:    token,
		Exposure: exposure,
		Private:  private,
		Scope: agentloop.AccessScope{
			UserID:           access.UserID,
			Allow:            access.Allow,
			ObjectID:         job.ChatID,
			ObjectVisibility: agentloop.Visibility(vis),
			VisibilityTeamID: team,
		},
	}
	var state, lineID string
	var round, claimRound, roundSeq int
	var createdAt time.Time
	var history []byte
	err = tx.QueryRow(ctx, `
		SELECT state, round, claim_round, round_seq, line_id::text, created_at, model, history
		FROM chat.runs WHERE id = $1::uuid AND chat_id = $2::uuid FOR UPDATE`,
		job.ReplyID, job.ChatID).Scan(&state, &round, &claimRound, &roundSeq, &lineID, &createdAt, &claim.Model, &history)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Claim{}, app.ErrNoLine
	}
	if err != nil {
		return app.Claim{}, fmt.Errorf("chat: claim run: %w", err)
	}
	if lineID != job.LineID {
		return app.Claim{}, app.ErrNoLine
	}
	claim.CreatedAt = createdAt

	switch {
	case state != "running" || round >= job.Round:
		claim.State = app.ClaimDone
		return claim, tx.Commit(ctx)
	case round != job.Round-1:
		return app.Claim{}, app.ErrNoLine
	case claimRound == job.Round:
		claim.State = app.ClaimRescue
	default:
		claim.State = app.ClaimGo
	}

	if _, err := tx.Exec(ctx, `UPDATE chat.runs SET claim = $2::uuid, claim_round = $3 WHERE id = $1::uuid`, job.ReplyID, token, job.Round); err != nil {
		return app.Claim{}, fmt.Errorf("chat: claim: %w", err)
	}
	if claim.State == app.ClaimGo && job.Round == 1 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat.deltas (chat_id, line_id, seq, kind, body)
			VALUES ($1::uuid, $2::uuid, 0, 'text', '')`, job.ChatID, job.ReplyID); err != nil {
			return app.Claim{}, fmt.Errorf("chat: reply start: %w", err)
		}
	}
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(seq), 0), COALESCE(sum(char_length(body)) FILTER (WHERE kind = 'text'), 0)
		FROM chat.deltas WHERE chat_id = $1::uuid AND line_id = $2::uuid`,
		job.ChatID, job.ReplyID).Scan(&claim.DeltaSeq, &claim.BodyRunes); err != nil {
		return app.Claim{}, fmt.Errorf("chat: claim deltas: %w", err)
	}
	if claim.State == app.ClaimGo {
		// The round starts here: notices of earlier rounds never name its model.
		roundSeq = claim.DeltaSeq
		if _, err := tx.Exec(ctx, `UPDATE chat.runs SET round_seq = $2 WHERE id = $1::uuid`, job.ReplyID, roundSeq); err != nil {
			return app.Claim{}, fmt.Errorf("chat: claim: %w", err)
		}
	}
	attempts, model, err := noticesTx(ctx, tx, job.ChatID, job.ReplyID, roundSeq)
	if err != nil {
		return app.Claim{}, err
	}
	claim.Attempts = attempts
	if model != "" {
		claim.Model = model
	}
	if claim.State == app.ClaimGo {
		claim.AllowedTools = map[string]bool{}
		if !claim.Exposure.Tainted {
			rows, err := tx.Query(ctx, `SELECT tool FROM chat.tool_allowances WHERE chat_id = $1::uuid AND user_id = $2::uuid`, job.ChatID, access.UserID)
			if err != nil {
				return app.Claim{}, fmt.Errorf("chat: claim allowances: %w", err)
			}
			for rows.Next() {
				var tool string
				if err := rows.Scan(&tool); err != nil {
					rows.Close()
					return app.Claim{}, fmt.Errorf("chat: claim allowance: %w", err)
				}
				claim.AllowedTools[tool] = true
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return app.Claim{}, fmt.Errorf("chat: claim allowances: %w", err)
			}
		}
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(max(seq), 0) FROM (
				SELECT seq FROM chat.turns WHERE chat_id = $1::uuid
				UNION ALL
				SELECT seq FROM chat.redacted_turn_metadata($1::uuid)
			) AS turns`, job.ChatID).Scan(&claim.TurnSeq); err != nil {
			return app.Claim{}, fmt.Errorf("chat: claim turns: %w", err)
		}
		if job.Round == 1 {
			lines, err := historyLinesTx(ctx, tx, job.ChatID, job.LineID)
			if err != nil {
				return app.Claim{}, err
			}
			claim.Lines = lines
		} else if history != nil {
			if err := json.Unmarshal(history, &claim.History); err != nil {
				return app.Claim{}, fmt.Errorf("chat: claim history: %w", err)
			}
		}
	}
	return claim, tx.Commit(ctx)
}

// noticesTx reads the status notices of a reply as the attempts they announce,
// and the next model of the last one after roundSeq: the current round's own.
// An earlier round's failover says nothing of the model a later round used.
func noticesTx(ctx context.Context, tx pgx.Tx, chatID, runID string, roundSeq int) ([]domain.Attempt, string, error) {
	rows, err := tx.Query(ctx, `
		SELECT seq, body FROM chat.deltas
		WHERE chat_id = $1::uuid AND line_id = $2::uuid AND kind = 'status'
		ORDER BY seq`, chatID, runID)
	if err != nil {
		return nil, "", fmt.Errorf("chat: reply notices: %w", err)
	}
	defer rows.Close()
	var attempts []domain.Attempt
	var model string
	for rows.Next() {
		var seq int
		var body string
		if err := rows.Scan(&seq, &body); err != nil {
			return nil, "", fmt.Errorf("chat: reply notice: %w", err)
		}
		var notice app.Notice
		if err := json.Unmarshal([]byte(body), &notice); err != nil {
			return nil, "", fmt.Errorf("chat: reply notice: %w", err)
		}
		attempts = append(attempts, domain.Attempt{Model: notice.FailedModel, Class: notice.Class})
		if seq > roundSeq {
			model = notice.NextModel
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("chat: reply notices: %w", err)
	}
	return attempts, model, nil
}

// PutDelta stores one row of a reply that is streaming, fenced by the claim:
// only the worker that owns the round may write. A write after a takeover gets
// ErrFenced.
func (s Store) PutDelta(ctx context.Context, access app.Access, claim app.Claim, seq int, kind, body string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM chat.runs
		WHERE id = $1::uuid AND chat_id = $2::uuid AND claim = $3::uuid AND state = 'running'
		FOR SHARE`, claim.Job.ReplyID, claim.Job.ChatID, claim.Token).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fenceErr(ctx, tx, claim.Job.ChatID, claim.Job.ReplyID)
	}
	if err != nil {
		return fmt.Errorf("chat: delta fence: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.deltas (chat_id, line_id, seq, kind, body)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, claim.Job.ChatID, claim.Job.ReplyID, seq, kind, body); err != nil {
		return fmt.Errorf("chat: delta: %w", err)
	}
	return tx.Commit(ctx)
}

// fenceErr tells a run that is there from one that is gone: ErrFenced for a
// run the editor still sees, goneOrSay otherwise.
func fenceErr(ctx context.Context, tx pgx.Tx, chatID, runID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat.runs WHERE id = $1::uuid AND chat_id = $2::uuid)`, runID, chatID).Scan(&exists); err != nil {
		return fmt.Errorf("chat: fence: %w", err)
	}
	if exists {
		return app.ErrFenced
	}
	return goneOrSay(ctx, tx, chatID)
}

// fenceTx holds the run row and checks that this worker still owns the round,
// under the chat's lock already held.
func fenceTx(ctx context.Context, tx pgx.Tx, claim app.Claim) error {
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM chat.runs
		WHERE id = $1::uuid AND chat_id = $2::uuid AND state = 'running'
		  AND claim = $3::uuid AND round = $4 - 1
		FOR UPDATE`, claim.Job.ReplyID, claim.Job.ChatID, claim.Token, claim.Job.Round).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrFenced
	}
	if err != nil {
		return fmt.Errorf("chat: fence: %w", err)
	}
	return nil
}

// CommitRound stores one round: its turns, the run's history and its costs,
// and the line when the round ends the reply. A hold is refused before any
// write. The write runs on a context that outlives the reply's own.
func (s Store) CommitRound(ctx context.Context, access app.Access, claim app.Claim, rc app.RoundCommit) error {
	if rc.Turn.Hold != nil || rc.Turn.Status == agentloop.TurnWaitingApproval {
		return app.ErrHold
	}
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChat(ctx, tx, claim.Job.ChatID); err != nil {
		return err
	}
	if err := fenceTx(ctx, tx, claim); err != nil {
		return err
	}
	if err := InsertTurnRows(ctx, tx, rc.Turn); err != nil {
		return err
	}
	if rc.Turn.Exposure.Tainted || rc.Turn.Exposure.PrivateRead {
		if _, err := tx.Exec(ctx, `SELECT chat.advance_chat_exposure($1::uuid, $2, $3)`,
			claim.Job.ChatID, rc.Turn.Exposure.Tainted, rc.Turn.Exposure.PrivateRead); err != nil {
			return fmt.Errorf("chat: exposure: %w", err)
		}
	}
	history := rc.Turn.History
	if history == nil {
		history = []agentloop.Message{}
	}
	rawHistory, err := json.Marshal(history)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE chat.runs SET round = $2, history = $3::jsonb, tokens = tokens + $4,
		  cost_micro_usd = cost_micro_usd + COALESCE($5::bigint, 0),
		  cost_known = cost_known AND $5::bigint IS NOT NULL, model = $6
		WHERE id = $1::uuid`, claim.Job.ReplyID, claim.Job.Round, rawHistory,
		rc.Turn.InputTokens+rc.Turn.OutputTokens, rc.Turn.ReportedCostMicroUSD, rc.Turn.Model); err != nil {
		return fmt.Errorf("chat: commit round: %w", err)
	}
	switch rc.End {
	case app.RoundNext:
		if err := putTailTx(ctx, tx, claim.Job.ChatID, claim.Job.ReplyID, rc.Tail); err != nil {
			return err
		}
		next := claim.Job
		next.Round++
		res, err := s.Enqueuer.InsertTx(ctx, tx, next, nil)
		if err != nil {
			return fmt.Errorf("chat: next round: %w", err)
		}
		if res.UniqueSkippedAsDuplicate {
			return errors.New("chat: next round was not queued")
		}
	case app.RoundDone:
		if err := doneLineTx(ctx, tx, claim.Job.ChatID, claim.Job.ReplyID, rc.Tail); err != nil {
			return err
		}
	case app.RoundFailed:
		rc.Fail.Tail = rc.Tail
		if err := failRunTx(ctx, tx, claim.Job.ChatID, claim.Job.ReplyID, rc.Fail, true); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Finish stores the failed line of a reply whose round did not commit, and
// deletes the reply's deltas in one transaction.
func (s Store) Finish(ctx context.Context, access app.Access, claim app.Claim, end app.ReplyEnd) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChat(ctx, tx, claim.Job.ChatID); err != nil {
		return err
	}
	if err := fenceTx(ctx, tx, claim); err != nil {
		return err
	}
	if err := failRunTx(ctx, tx, claim.Job.ChatID, claim.Job.ReplyID, end, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Stop stores partial reply text and fences the active worker in one transaction.
func (s Store) Stop(ctx context.Context, access app.Access, chatID string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockChat(ctx, tx, chatID); err != nil {
		return err
	}
	var runID, prev string
	err = tx.QueryRow(ctx, `SELECT id::text, state FROM chat.runs WHERE chat_id = $1::uuid AND state IN ('running', 'waiting') FOR UPDATE`, chatID).Scan(&runID, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return fmt.Errorf("chat: stop run: %w", err)
	}
	text, err := replyTextTx(ctx, tx, chatID, runID, app.Pending{})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO chat.lines (id, chat_id, seq, role, body, model, tokens, cost_micro_usd, status, reply_to) SELECT run.id, run.chat_id, (SELECT COALESCE(max(seq), 0) + 1 FROM chat.lines WHERE chat_id = run.chat_id), 'assistant', left($2, 32000), CASE WHEN run.model <> '' THEN run.model ELSE 'stopped' END, run.tokens, CASE WHEN run.cost_known THEN run.cost_micro_usd END, 'stopped', run.line_id FROM chat.runs AS run WHERE run.id = $1::uuid`, runID, text); err != nil {
		return writeErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chat.deltas WHERE chat_id = $1::uuid AND line_id = $2::uuid`, chatID, runID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT chat.close_waiting_run($1::uuid, 'stopped', NULL)`, chatID).Scan(new(any)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{Action: audit.ChatStop, Outcome: audit.Success, TargetID: runID, Changes: audit.Changes{Fields: []string{"status"}, Values: map[string]audit.Change{"status": {From: prev, To: domain.StatusStopped}}}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// putTailTx stores the one tail of text a round never flushed as a delta,
// unless it is already there, so the reply's body is read in seq order.
func putTailTx(ctx context.Context, tx pgx.Tx, chatID, runID string, tail app.Pending) error {
	if tail.Text == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.deltas (chat_id, line_id, seq, kind, body)
		VALUES ($1::uuid, $2::uuid, $3, 'text', $4)
		ON CONFLICT (line_id, seq) DO NOTHING`, chatID, runID, tail.Seq, tail.Text); err != nil {
		return fmt.Errorf("chat: tail: %w", err)
	}
	return nil
}

// replyTextTx is the body of a reply, persisted deltas plus the one tail, each
// part exactly once.
func replyTextTx(ctx context.Context, tx pgx.Tx, chatID, runID string, tail app.Pending) (string, error) {
	var text string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(string_agg(body, '' ORDER BY seq), '')
		       || CASE WHEN $3 <> '' AND NOT EXISTS (
		            SELECT 1 FROM chat.deltas WHERE chat_id = $1::uuid AND line_id = $2::uuid AND seq = $4)
		          THEN $3 ELSE '' END
		FROM chat.deltas WHERE chat_id = $1::uuid AND line_id = $2::uuid AND kind = 'text'`,
		chatID, runID, tail.Text, tail.Seq).Scan(&text)
	if err != nil {
		return "", fmt.Errorf("chat: reply text: %w", err)
	}
	return text, nil
}

// doneLineTx stores the done line of a reply and ends its run.
func doneLineTx(ctx context.Context, tx pgx.Tx, chatID, runID string, tail app.Pending) error {
	text, err := replyTextTx(ctx, tx, chatID, runID, tail)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.lines (id, chat_id, seq, role, body, model, tokens, cost_micro_usd, status, reply_to)
		SELECT run.id, run.chat_id, (SELECT COALESCE(max(seq), 0) + 1 FROM chat.lines WHERE chat_id = run.chat_id),
		       'assistant', left($2, 32000), run.model, run.tokens,
		       CASE WHEN run.cost_known THEN run.cost_micro_usd END, 'done', run.line_id
		FROM chat.runs AS run WHERE run.id = $1::uuid`, runID, text); err != nil {
		return writeErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chat.deltas WHERE chat_id = $1::uuid AND line_id = $2::uuid`, chatID, runID); err != nil {
		return fmt.Errorf("chat: deltas: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE chat.runs SET state = 'done', claim = NULL, history = NULL, finished_at = now() WHERE id = $1::uuid`, runID); err != nil {
		return fmt.Errorf("chat: finish run: %w", err)
	}
	return nil
}

// failRunTx stores the failed line of a reply and ends its run. keepCost keeps
// the run's cost on the line; a takeover or an uncommitted round leaves it
// unknown.
func failRunTx(ctx context.Context, tx pgx.Tx, chatID, runID string, end app.ReplyEnd, keepCost bool) error {
	text, err := replyTextTx(ctx, tx, chatID, runID, end.Tail)
	if err != nil {
		return err
	}
	attempts, err := json.Marshal(append([]domain.Attempt{}, end.Attempts...))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.lines (id, chat_id, seq, role, body, model, tokens, cost_micro_usd, status, reply_to, attempts)
		SELECT run.id, run.chat_id, (SELECT COALESCE(max(seq), 0) + 1 FROM chat.lines WHERE chat_id = run.chat_id),
		       'assistant', left($2, 32000), $3, run.tokens,
		       CASE WHEN $4 AND run.cost_known THEN run.cost_micro_usd END, 'failed', run.line_id, $5::jsonb
		FROM chat.runs AS run WHERE run.id = $1::uuid`, runID, text, end.Model, keepCost, attempts); err != nil {
		return writeErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chat.deltas WHERE chat_id = $1::uuid AND line_id = $2::uuid`, chatID, runID); err != nil {
		return fmt.Errorf("chat: deltas: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE chat.runs SET state = 'failed', claim = NULL, history = NULL, finished_at = now() WHERE id = $1::uuid`, runID); err != nil {
		return fmt.Errorf("chat: finish run: %w", err)
	}
	return nil
}

// Sweep deletes the deltas that are older than two hours. The service runs it
// on its own schedule, so its event carries the system actor with a service
// credential.
func (s Store) Sweep(ctx context.Context) (int64, error) {
	if s.Pool == nil {
		return 0, fmt.Errorf("chat: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var swept int64
	if err := tx.QueryRow(ctx, `SELECT chat.sweep_deltas()`).Scan(&swept); err != nil {
		return 0, fmt.Errorf("chat: sweep: %w", err)
	}
	// A sweep that deleted nothing changed nothing, so it records nothing.
	if swept == 0 {
		return 0, tx.Commit(ctx)
	}
	if err := audit.Record(audit.WithRunner(ctx, audit.CredentialService), tx, audit.Event{
		Action:  audit.ChatSweep,
		Outcome: audit.Success,
		Changes: audit.Changes{
			Fields: []string{"rows"},
			Values: map[string]audit.Change{"rows": {To: swept}},
		},
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return swept, nil
}

// FailReply stores the failed reply of a run for an author who can no longer
// write the chat, or for a job that could not start, and deletes the reply's
// deltas. why is access or unwritten. It runs without the author's access and
// reports false when the run fenced it out.
func (s Store) FailReply(ctx context.Context, job app.ReplyJobArgs, token, why string) (bool, error) {
	if s.Pool == nil {
		return false, fmt.Errorf("chat: no database")
	}
	var outcome string
	if err := s.Pool.QueryRow(ctx, `SELECT chat.fail_reply($1::uuid, $2, NULLIF($3, '')::uuid, $4)`, job.ReplyID, job.Round, token, why).Scan(&outcome); err != nil {
		return false, fmt.Errorf("chat: fail reply: %w", err)
	}
	switch outcome {
	case "stored":
		return true, nil
	case "fenced":
		return false, nil
	case "none":
		return false, nil
	}
	return false, fmt.Errorf("chat: fail reply: no such reply")
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

func (s Store) IsPrivate(ctx context.Context, access app.Access, chatID string) (bool, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var private bool
	err = tx.QueryRow(ctx, `SELECT visibility = 'private' AND NOT EXISTS (SELECT 1 FROM identity.shares WHERE object_type = 'chat' AND object_id = chat.chats.id) FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&private)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, app.ErrGone
	}
	if err != nil {
		return false, err
	}
	return private, tx.Commit(ctx)
}

// Retry asks again for the reply to the user line that a failed assistant line
// left unanswered. The failed line stays; it is hidden once a later reply to
// the same user line succeeds. With no picks given (nil) the picks stored on
// the user line are used again; picks given, even all empty, replace them as
// a whole.
func (s Store) Retry(ctx context.Context, access app.Access, chatID, lineID string, given *app.Picks) error {
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
	if err := failStaleRuns(ctx, tx, chatID); err != nil {
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
	if role != domain.RoleAssistant || (status != domain.StatusFailed && status != domain.StatusStopped) || userLine == nil {
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
	var picks app.Picks
	if given != nil {
		picks = *given
	} else {
		// Nothing given: the stored picks again. The pin and the agent are the
		// author's own, so anyone else retrying the line gets the route only.
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(route, ''),
			       CASE WHEN author_id = $2::uuid THEN COALESCE(pin_account_id::text, '') ELSE '' END,
			       CASE WHEN author_id = $2::uuid THEN COALESCE(pin_model, '') ELSE '' END,
			       CASE WHEN author_id = $2::uuid THEN COALESCE(agent_id::text, '') ELSE '' END
			FROM chat.lines WHERE id = $1::uuid`, *userLine, access.UserID).Scan(&picks.Route, &picks.PinAccount, &picks.PinModel, &picks.AgentID)
		if err != nil {
			return fmt.Errorf("chat: retry picks: %w", err)
		}
	}
	if err := s.reserveRun(ctx, tx, access, chatID, *userLine, picks); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ChatRetry,
		Outcome:  audit.Success,
		TargetID: *userLine,
		Changes:  pickChanges(picks),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
