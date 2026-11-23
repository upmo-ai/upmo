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
func (o chatOutputs) SaveOutput(ctx context.Context, _ *agentloop.RunState, title, body string, untrusted bool) (string, error) {
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
		INSERT INTO chat.findings (chat_id, line_id, kind, title, body, untrusted)
		VALUES ($1::uuid, $2::uuid, 'tool_output', $3, $4, $5) RETURNING id::text`,
		o.chatID, o.replyID, title, capOutput(body), untrusted).Scan(&id)
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
		SELECT body, untrusted FROM chat.findings
		WHERE id = $1::uuid AND chat_id = $2::uuid AND kind = 'tool_output'`,
		id, o.chatID).Scan(&body, &untrusted)
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
