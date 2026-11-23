package postgres

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

const outputCapBytes = 262144

var errNoOutput = errors.New("no such output")

// chatOutputs stores and reads the tool output of one reply, scoped to its
// chat.
type chatOutputs struct {
	store           Store
	chatID, replyID string
	access          func() (app.Access, error)
}

// Outputs returns the agentloop.OutputStore of a reply: SaveOutput and
// RetrieveOutput on chat.findings, under the access as it is at call time.
func (s Store) Outputs(chatID, replyID string, access func() (app.Access, error)) agentloop.OutputStore {
	return chatOutputs{store: s, chatID: chatID, replyID: replyID, access: access}
}

// SaveOutput stores one tool output of the reply.
func (o chatOutputs) SaveOutput(ctx context.Context, run *agentloop.RunState, title, body string, untrusted bool) (string, error) {
	a, err := o.access()
	if err != nil {
		return "", err
	}
	tx, err := o.store.begin(ctx, a)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.findings (chat_id, line_id, kind, title, body, untrusted, private_read, author_id)
		VALUES ($1::uuid, $2::uuid, 'tool_output', $3, $4, $5, $6, NULLIF($7, '')::uuid) RETURNING id::text`,
		o.chatID, o.replyID, title, capOutput(body), untrusted, run != nil && run.Exposure.PrivateRead, a.UserID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("chat: output: %w", err)
	}
	return id, tx.Commit(ctx)
}

// RetrieveOutput reads one tool output of the reply's chat.
func (o chatOutputs) RetrieveOutput(ctx context.Context, _ *agentloop.RunState, id string) (string, bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", false, errNoOutput
	}
	a, err := o.access()
	if err != nil {
		return "", false, err
	}
	tx, err := o.store.begin(ctx, a)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var body string
	var untrusted bool
	err = tx.QueryRow(ctx, `
		SELECT f.body, f.untrusted FROM chat.findings AS f JOIN chat.chats AS c ON c.id = f.chat_id
		WHERE f.id = $1::uuid AND f.chat_id = $2::uuid AND f.kind = 'tool_output'
		AND (NOT f.private_read OR (f.author_id = $3::uuid AND c.visibility = 'private' AND NOT EXISTS (SELECT 1 FROM identity.shares s WHERE s.object_type = 'chat' AND s.object_id = c.id)))`,
		id, o.chatID, a.UserID).Scan(&body, &untrusted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, errNoOutput
	}
	if err != nil {
		return "", false, fmt.Errorf("chat: output: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return body, untrusted, nil
}

// capOutput cuts an output down to outputCapBytes with a marker, on a rune
// boundary.
func capOutput(body string) string {
	if len(body) <= outputCapBytes {
		return body
	}
	marker := fmt.Sprintf("\n[output truncated: %d bytes in all]", len(body))
	keep := outputCapBytes - len(marker)
	for keep > 0 && !utf8.RuneStart(body[keep]) {
		keep--
	}
	return body[:keep] + marker
}
