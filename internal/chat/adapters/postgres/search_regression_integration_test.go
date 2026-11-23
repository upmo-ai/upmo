//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/app"
)

func TestSearchEscapesLikeWildcardsInTitleQuery(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)

	budget, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Rename(ctx, store, ownerAccess, budget.ID, "Budget 100%"); err != nil {
		t.Fatal(err)
	}
	plan, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Rename(ctx, store, ownerAccess, plan.ID, "Budget plan"); err != nil {
		t.Fatal(err)
	}

	got, err := app.Search(ctx, store, ownerAccess, "100%")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != budget.ID {
		t.Fatalf(`Search("100%%") = %+v`, got)
	}
	got, err = app.Search(ctx, store, ownerAccess, "_")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf(`Search("_") = %+v`, got)
	}
}

func TestSearchIgnoresFailedAssistantLineSupersededByRetry(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "retry this", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	chat, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	userLine := chat.Lines[0].ID
	const failedReply = "018f3b2c-7a10-7c4a-8e11-cccccccccc01"
	const sentinel = "quasarlexicon"
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (id, chat_id, seq, role, body, model, status, reply_to, attempts)
		SELECT $1::uuid, $2::uuid, MAX(seq) + 1, 'assistant', $4, 'm1', 'failed', $3::uuid, '[]'::jsonb
		FROM chat.lines WHERE chat_id = $2::uuid`, failedReply, opened.ID, userLine, sentinel); err != nil {
		t.Fatal(err)
	}
	settle(t, owner, opened.ID)
	if err := app.Retry(ctx, store, ownerAccess, opened.ID, failedReply, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, reply_to, status)
		SELECT $1::uuid, MAX(seq) + 1, 'assistant', 'Recovered response', 'm1', $2::uuid, 'done'
		FROM chat.lines WHERE chat_id = $1::uuid`, opened.ID, userLine); err != nil {
		t.Fatal(err)
	}

	got, err := app.Search(ctx, store, ownerAccess, sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Search(%q) = %+v", sentinel, got)
	}
}
