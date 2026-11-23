package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
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
	// ReplaceChatGrants runs identity's grant replacement inside a transaction
	// the chat store owns, so grants, visibility and chat.share commit or roll
	// back together.
	ReplaceChatGrants func(ctx context.Context, tx pgx.Tx, chatID string, grants []domain.ShareGrant, grantedBy string) error
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
	lineUUID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	lineID := lineUUID.String()
	atts := slices.Clone(picks.Attachments)
	var rawAttachments []byte
	if len(atts) > 0 {
		for i := range atts {
			var findingID string
			if err := tx.QueryRow(ctx, `
				INSERT INTO chat.findings (chat_id, line_id, kind, title, body, source_ref, untrusted, author_id)
				VALUES ($1::uuid, $2::uuid, 'attachment', $3, $4, $5, true, $6::uuid)
				RETURNING id::text`,
				chatID, lineID, atts[i].Name, atts[i].Extracted, atts[i].SHA256, access.UserID,
			).Scan(&findingID); err != nil {
				return fmt.Errorf("chat: attachment finding: %w", err)
			}
			atts[i].ID = findingID
		}
		rawAttachments, err = json.Marshal(atts)
		if err != nil {
			return fmt.Errorf("chat: marshal attachments: %w", err)
		}
	}
	var seq int
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.lines (id, chat_id, seq, body, agent_id, route, pin_account_id, pin_model, author_id, attachments)
		SELECT $1::uuid, $2::uuid,
		       COALESCE((SELECT MAX(seq) FROM chat.lines WHERE chat_id = $2::uuid), 0) + 1,
		       $3, $4::uuid, $5, $6::uuid, $7, $8::uuid, $9::jsonb
		RETURNING seq`, lineID, chatID, body,
		orNull(picks.AgentID), orNull(picks.Route), orNull(picks.PinAccount), orNull(picks.PinModel), access.UserID, rawAttachments).Scan(&seq)
	if err != nil {
		return writeErr(err)
	}
	if len(atts) > 0 {
		if _, err := tx.Exec(ctx, `SELECT chat.advance_chat_exposure($1::uuid, true, false)`, chatID); err != nil {
			return fmt.Errorf("chat: taint attachment: %w", err)
		}
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
	if _, err := tx.Exec(ctx, `SELECT chat.fail_waiting_runs($1::uuid)`, chatID); err != nil {
		return fmt.Errorf("chat: stale holds: %w", err)
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
	fields := []string{"agent_id", "model_id", "model_provider", "route"}
	values := map[string]audit.Change{
		"route":          {To: picks.Route},
		"model_provider": {To: picks.PinAccount},
		"model_id":       {To: picks.PinModel},
		"agent_id":       {To: picks.AgentID},
	}
	if len(picks.Attachments) > 0 {
		fields = append(fields, "attachment_count")
		values["attachment_count"] = audit.Change{To: len(picks.Attachments)}
	}
	return audit.Changes{Fields: fields, Values: values}
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
		SELECT chat.title,
		       chat.id::text,
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
		var customTitle, firstUser, newest string
		if err := rows.Scan(&customTitle, &summary.ID, &firstUser, &newest, &summary.LastActivity); err != nil {
			return nil, fmt.Errorf("chat: list: %w", err)
		}
		summary.Title = domain.DisplayTitle(customTitle, firstUser)
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

// Search lists the chats under row security whose title or line body matches
// the query, newest activity first, at most 100. A chat that matches by body
// carries its matching line as the preview.
func (s Store) Search(ctx context.Context, access app.Access, query string) ([]app.ChatSummary, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		WITH q AS (
		  SELECT plainto_tsquery('english', $1) AS tsq
		)
		SELECT chat.id::text,
		       chat.title,
		       COALESCE((
		         SELECT left(line.body, 60) FROM chat.lines AS line
		         WHERE line.chat_id = chat.id AND line.role = 'user'
		         ORDER BY line.seq
		         LIMIT 1
		       ), '') AS first_user_line,
		       COALESCE((
		         SELECT line.body FROM chat.lines AS line, q
		         WHERE line.chat_id = chat.id
		           AND NOT (`+superseded+`)
		           AND numnode(q.tsq) > 0
		           AND line.body_tsv @@ q.tsq
		         ORDER BY line.seq DESC
		         LIMIT 1
		       ), (
		         SELECT line.body FROM chat.lines AS line
		         WHERE line.chat_id = chat.id AND NOT (`+superseded+`)
		         ORDER BY line.seq DESC
		         LIMIT 1
		       ), '') AS preview_line,
		       COALESCE((
		         SELECT line.at FROM chat.lines AS line
		         WHERE line.chat_id = chat.id
		         ORDER BY line.seq DESC
		         LIMIT 1
		       ), chat.created_at) AS last_activity
		FROM chat.chats AS chat, q
		WHERE (chat.title <> '' AND chat.title ILIKE $2 ESCAPE '\')
		   OR (
		        chat.title = ''
		        AND COALESCE((
		          SELECT left(line.body, 60) FROM chat.lines AS line
		          WHERE line.chat_id = chat.id AND line.role = 'user'
		          ORDER BY line.seq
		          LIMIT 1
		        ), '') ILIKE $2 ESCAPE '\'
		      )
		   OR (
		        numnode(q.tsq) > 0
		        AND EXISTS (
		          SELECT 1 FROM chat.lines AS line
		          WHERE line.chat_id = chat.id
		            AND NOT (`+superseded+`)
		            AND line.body_tsv @@ q.tsq
		        )
		      )
		ORDER BY last_activity DESC, chat.id DESC
		LIMIT 100`, query, "%"+escapeLike(query)+"%")
	if err != nil {
		return nil, fmt.Errorf("chat: search: %w", err)
	}
	defer rows.Close()

	out := []app.ChatSummary{}
	for rows.Next() {
		var summary app.ChatSummary
		var firstUser, preview string
		if err := rows.Scan(&summary.ID, &summary.Title, &firstUser, &preview, &summary.LastActivity); err != nil {
			return nil, fmt.Errorf("chat: search: %w", err)
		}
		summary.Title = domain.DisplayTitle(summary.Title, firstUser)
		summary.Preview = domain.Preview(preview)
		summary.LastActivity = summary.LastActivity.UTC()
		out = append(out, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: search: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// escapeLike doubles the escape, percent and underscore characters of a LIKE
// pattern, so the title ILIKE matches them literally.
func escapeLike(query string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
}

// Read returns the lines in order.
func (s Store) Read(ctx context.Context, access app.Access, chatID string) (app.Chat, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Chat{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tainted, privateRead, canEdit, canDelete bool
	var customTitle, firstUser string
	err = tx.QueryRow(ctx, `
		SELECT title, tainted, private_read,
		       identity.may('edit', object_type, owner_user_id, visibility, visibility_team_id, id),
		       owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
		         AND 'chat:delete' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')),
		       COALESCE((SELECT body FROM chat.lines AS line WHERE line.chat_id = $1::uuid AND line.role = 'user' ORDER BY line.seq LIMIT 1), '')
		FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&customTitle, &tainted, &privateRead, &canEdit, &canDelete, &firstUser)
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
	rows.Close()
	var privateUnshared bool
	if err := tx.QueryRow(ctx, `SELECT visibility = 'private' AND NOT EXISTS (SELECT 1 FROM identity.shares WHERE object_type = 'chat' AND object_id = chat.chats.id) FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&privateUnshared); err != nil {
		return app.Chat{}, err
	}
	turns, err := turnsForChatTx(ctx, tx, chatID, access.UserID, privateUnshared)
	if err != nil {
		return app.Chat{}, err
	}
	for i := range lines {
		lines[i].Turns = turns[lines[i].ID]
	}
	var hold app.Hold
	var calls [][]byte
	err = tx.QueryRow(ctx, `SELECT id::text, COALESCE(line_id::text, ''), reason, action, preview, untrusted_origin, calls FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending' AND user_id = $2::uuid`, chatID, access.UserID).Scan(&hold.ID, &hold.LineID, &hold.Reason, &hold.Action, &hold.Preview, &hold.UntrustedOrigin, &calls)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return app.Chat{}, fmt.Errorf("chat: hold: %w", err)
	}
	if err == nil {
		hold.Calls = make([]app.HoldCall, 0, len(calls))
		hold.CanAllowForChat = !tainted
		for _, raw := range calls {
			var call agentloop.HeldCall
			if err := json.Unmarshal(raw, &call); err != nil {
				return app.Chat{}, fmt.Errorf("chat: hold calls: %w", err)
			}
			hold.Calls = append(hold.Calls, app.HoldCall{CallID: call.CallID, ToolName: call.ToolName, Op: string(call.Op), Action: call.Action, Preview: call.Preview, UntrustedOrigin: call.UntrustedOrigin})
			hold.CanAllowForChat = hold.CanAllowForChat && app.CanAllowForChat(call.ToolName, call.Op, tainted)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Chat{}, err
	}
	chat := app.Chat{ID: chatID, Title: domain.DisplayTitle(customTitle, firstUser), Lines: lines, Tainted: tainted, PrivateRead: privateRead, CanEdit: canEdit, CanDelete: canDelete}
	if hold.ID != "" {
		chat.Hold = &hold
	}
	return chat, nil
}

func turnsForChatTx(ctx context.Context, tx pgx.Tx, chatID, viewerUserID string, isPrivateUnshared bool) (map[string][]app.Turn, error) {
	rows, err := tx.Query(ctx, `
		SELECT COALESCE(line_id::text, ''), seq, role, body, tool_calls, tool_name, tool_id, is_error, offloaded, tainted, private_read, author_id::text
		FROM chat.turns WHERE chat_id = $1::uuid
		UNION ALL
		SELECT COALESCE(line_id::text, ''), seq, role, ''::text, NULL::jsonb, tool_name, tool_id, is_error, offloaded, tainted, true, NULL::text
		FROM chat.redacted_turn_metadata($1::uuid)
		ORDER BY seq`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]app.Turn{}
	for rows.Next() {
		var lineID string
		var turn app.Turn
		var calls []byte
		var privateRead bool
		var authorID *string
		if err := rows.Scan(&lineID, &turn.Seq, &turn.Role, &turn.Body, &calls, &turn.ToolName, &turn.ToolID, &turn.IsError, &turn.Offloaded, &turn.Tainted, &privateRead, &authorID); err != nil {
			return nil, err
		}
		turn.ToolCalls = calls
		if privateRead && (authorID == nil || *authorID != viewerUserID || !isPrivateUnshared) {
			turn.Body = ""
			turn.ToolCalls = nil
		}
		out[lineID] = append(out[lineID], turn)
	}
	return out, rows.Err()
}

// Delete removes an owned chat and all of its chat rows.
func (s Store) Delete(ctx context.Context, access app.Access, chatID string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT chat.delete_chat($1::uuid)`, chatID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "P0002":
				return app.ErrGone
			case "42501":
				return app.ErrAccess
			}
		}
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{Action: audit.ChatDelete, Outcome: audit.Success, TargetID: chatID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Rename changes a chat's custom title under row security: a chat this
// person can see but not edit is locked out, so it answers ErrSay.
func (s Store) Rename(ctx context.Context, access app.Access, chatID, title string) (app.ChatSummary, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.ChatSummary{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockChat(ctx, tx, chatID); err != nil {
		return app.ChatSummary{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE chat.chats SET title = $2 WHERE id = $1::uuid`, chatID, title)
	if err != nil {
		return app.ChatSummary{}, fmt.Errorf("chat: rename: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ChatSummary{}, goneOrSay(ctx, tx, chatID)
	}
	// The title is content: the event records the field name alone.
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ChatUpdate,
		Outcome:  audit.Success,
		TargetID: chatID,
		Changes:  audit.Changes{Fields: []string{"title"}},
	}); err != nil {
		return app.ChatSummary{}, err
	}
	var newest string
	var lastActivity time.Time
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE((SELECT body FROM chat.lines AS line WHERE line.chat_id = chat.id ORDER BY line.seq DESC LIMIT 1), ''),
		       COALESCE((SELECT line.at FROM chat.lines AS line WHERE line.chat_id = chat.id ORDER BY line.seq DESC LIMIT 1), chat.created_at)
		FROM chat.chats AS chat WHERE chat.id = $1::uuid`, chatID).Scan(&newest, &lastActivity); err != nil {
		return app.ChatSummary{}, fmt.Errorf("chat: rename: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.ChatSummary{}, err
	}
	return app.ChatSummary{ID: chatID, Title: title, Preview: domain.Preview(newest), LastActivity: lastActivity.UTC()}, nil
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
	return `line.id::text, line.seq, line.role, line.body,
	line.author_id::text,
	(SELECT person.name FROM identity.users AS person WHERE person.id = line.author_id),
	(SELECT CASE
		WHEN person.avatar_key IS NOT NULL AND person.avatar_key <> ''
		THEN '/api/users/' || line.author_id::text || '/avatar?v=' || person.avatar_key
		ELSE NULL
	END FROM identity.users AS person WHERE person.id = line.author_id),
	line.model, line.tokens, line.cost_micro_usd, line.at,
	line.status, line.reply_to::text, line.attempts, line.attachments,
	line.route,
	CASE WHEN line.author_id = ` + viewer + ` THEN line.pin_account_id::text END,
	line.pin_model,
	CASE WHEN line.author_id = ` + viewer + ` THEN line.agent_id::text END,
	(line.role = 'user' AND NOT EXISTS (SELECT 1 FROM chat.lines AS reply WHERE reply.reply_to = line.id))`
}

// superseded holds for a failed reply that a later reply to the same user
// line has answered, on a chat.lines row named line. Such a line stays
// stored but is hidden from the thread and from the history.
const superseded = `line.status IN ('failed', 'stopped') AND EXISTS (
	SELECT 1 FROM chat.lines AS later
	WHERE later.reply_to = line.reply_to AND later.status = 'done' AND later.seq > line.seq
)`

// scanLine reads the lineColumnsFor of a row, after any lead columns.
func scanLine(rows pgx.Rows, lead ...any) (app.Line, error) {
	var line app.Line
	var attempts []byte
	var attachments []byte
	dest := append(lead, &line.ID, &line.Seq, &line.Role, &line.Body,
		&line.AuthorID, &line.AuthorName, &line.AuthorAvatar,
		&line.Model, &line.Tokens, &line.CostMicroUSD, &line.At,
		&line.Status, &line.ReplyTo, &attempts, &attachments, &line.Route, &line.PinAccount, &line.PinModel, &line.AgentID, &line.Pending)
	if err := rows.Scan(dest...); err != nil {
		return app.Line{}, fmt.Errorf("chat: line: %w", err)
	}
	if attempts != nil {
		if err := json.Unmarshal(attempts, &line.Attempts); err != nil {
			return app.Line{}, fmt.Errorf("chat: line attempts: %w", err)
		}
	}
	if attachments != nil {
		if err := json.Unmarshal(attachments, &line.Attachments); err != nil {
			return app.Line{}, fmt.Errorf("chat: line attachments: %w", err)
		}
	}
	return line, nil
}
