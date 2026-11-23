//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func seedChatUser(t *testing.T, owner *pgxpool.Pool, id, name, roleKey string, avatarKey *string) {
	t.Helper()
	email := strings.ToLower(name) + "@example.com"
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at, avatar_key)
		SELECT $1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $3, role.id, now(), $5
		FROM identity.roles AS role
		WHERE role.key = $4`, id, email, name, roleKey, avatarKey); err != nil {
		t.Fatal(err)
	}
}

func TestLinesInsertRLSEnforcesAuthorID(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}

	appPool := pgtest.App(t, owner)
	execAsOwner := func(sql string, args ...any) error {
		tx, err := appPool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `
			SELECT set_config('upmo.user_id', $1, true),
			       set_config('upmo.allow', $2, true),
			       set_config('upmo.teams', '{}', true),
			       set_config('upmo.shared_view', '{}', true),
			       set_config('upmo.shared_edit', '{}', true)`,
			ownerUser, ownerAccess.Allow); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	// role = 'user' with author_id IS NULL must be rejected by lines_insert RLS.
	if err := execAsOwner(`
		INSERT INTO chat.lines (chat_id, seq, role, body, author_id)
		VALUES ($1::uuid, 1, 'user', 'spoofed null author', NULL)`, opened.ID); err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("user insert with NULL author_id = %v, want row-level security violation", err)
	}

	// role = 'user' with another user's author_id must be rejected by lines_insert RLS.
	if err := execAsOwner(`
		INSERT INTO chat.lines (chat_id, seq, role, body, author_id)
		VALUES ($1::uuid, 1, 'user', 'spoofed other author', $2::uuid)`, opened.ID, otherUser); err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("user insert with other author_id = %v, want row-level security violation", err)
	}

	// role = 'user' with matching author_id succeeds.
	if err := execAsOwner(`
		INSERT INTO chat.lines (chat_id, seq, role, body, author_id)
		VALUES ($1::uuid, 1, 'user', 'valid user line', $2::uuid)`, opened.ID, ownerUser); err != nil {
		t.Fatalf("user insert with matching author_id = %v", err)
	}

	// role = 'assistant' with author_id IS NULL succeeds.
	if err := execAsOwner(`
		INSERT INTO chat.lines (chat_id, seq, role, body, model, author_id)
		VALUES ($1::uuid, 2, 'assistant', 'valid assistant line', 'm1', NULL)`, opened.ID); err != nil {
		t.Fatalf("assistant insert with NULL author_id = %v", err)
	}
}

func TestReadReturnsAuthorIDNameAndAvatar(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	avatar := "avatars/grace.png"
	seedChatUser(t, owner, ownerUser, "Ada Lovelace", "owner", nil)
	seedChatUser(t, owner, otherUser, "Grace Hopper", "member", &avatar)

	store := quietStore(t, owner)
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "owner line", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	settle(t, owner, opened.ID)
	var firstLineID string
	if err := owner.QueryRow(ctx, `SELECT id::text FROM chat.lines WHERE chat_id = $1::uuid AND seq = 1`, opened.ID).Scan(&firstLineID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, reply_to)
		VALUES ($1::uuid, 2, 'assistant', 'assistant reply', 'm1', $2::uuid)`, opened.ID, firstLineID); err != nil {
		t.Fatal(err)
	}
	editor := editorOf(opened.ID)
	if err := app.Say(ctx, store, editor, opened.ID, "editor follow-up", app.Picks{}); err != nil {
		t.Fatal(err)
	}

	got, err := app.Read(ctx, store, editor, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(got.Lines))
	}
	l1, l2, l3 := got.Lines[0], got.Lines[1], got.Lines[2]
	if l1.AuthorID == nil || *l1.AuthorID != ownerUser || l1.AuthorName == nil || *l1.AuthorName != "Ada Lovelace" || l1.AuthorAvatar != nil {
		t.Fatalf("line 1 author = (%v, %v, %v)", l1.AuthorID, l1.AuthorName, l1.AuthorAvatar)
	}
	if l2.AuthorID != nil || l2.AuthorName != nil || l2.AuthorAvatar != nil {
		t.Fatalf("line 2 assistant author = (%v, %v, %v), want all nil", l2.AuthorID, l2.AuthorName, l2.AuthorAvatar)
	}
	wantAvatar := "/api/users/" + otherUser + "/avatar?v=" + avatar
	if l3.AuthorID == nil || *l3.AuthorID != otherUser || l3.AuthorName == nil || *l3.AuthorName != "Grace Hopper" || l3.AuthorAvatar == nil || *l3.AuthorAvatar != wantAvatar {
		t.Fatalf("line 3 author = (%v, %v, %v), want avatar %q", l3.AuthorID, l3.AuthorName, l3.AuthorAvatar, wantAvatar)
	}
}

func TestSaveShareBlocksActiveAndWaitingRunsAndClearsStale(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	seedChatUser(t, owner, ownerUser, "Ada", "owner", nil)
	seedChatUser(t, owner, otherUser, "Bob", "member", nil)

	pool := pgtest.App(t, owner)
	members := identity.Members(pool, nil, "", nil, nil)
	store := quietStore(t, owner)
	store.ReplaceChatGrants = func(ctx context.Context, tx pgx.Tx, chatID string, grants []domain.ShareGrant, grantedBy string) error {
		by, err := identity.ParseUserID(grantedBy)
		if err != nil {
			return err
		}
		writes := make([]identity.GrantWrite, 0, len(grants))
		for _, g := range grants {
			writes = append(writes, identity.GrantWrite{SubjectType: g.SubjectType, SubjectID: g.SubjectID, Level: g.Role})
		}
		return members.ReplaceChatGrantsTx(ctx, tx, chatID, writes, by)
	}

	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	grants := []domain.ShareGrant{{SubjectType: domain.SharePerson, SubjectID: otherUser, Role: domain.ShareEditor}}

	// Non-owner editor cannot SaveShare.
	if err := store.SaveShare(ctx, editorOf(opened.ID), opened.ID, domain.VisibilityEveryone, "", nil, otherUser); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("editor SaveShare = %v, want ErrAccess", err)
	}

	// Start a run (state = 'running'): SaveShare must return ErrBusy.
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "running message", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveShare(ctx, ownerAccess, opened.ID, domain.VisibilityPrivate, "", grants, ownerUser); !errors.Is(err, app.ErrBusy) {
		t.Fatalf("running SaveShare = %v, want ErrBusy", err)
	}

	// Transition run to 'waiting' with a pending hold: SaveShare must return ErrBusy.
	var runID string
	if err := owner.QueryRow(ctx, `SELECT id::text FROM chat.runs WHERE chat_id = $1::uuid`, opened.ID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE chat.runs SET state = 'waiting' WHERE id = $1::uuid`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.holds (chat_id, line_id, user_id, reason, action, preview, state, hold_turn_seq, calls)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'ask', 'open_task', 'preview', 'pending', 1, ARRAY['{}'::jsonb])`,
		opened.ID, runID, ownerUser); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveShare(ctx, ownerAccess, opened.ID, domain.VisibilityPrivate, "", grants, ownerUser); !errors.Is(err, app.ErrBusy) {
		t.Fatalf("waiting SaveShare = %v, want ErrBusy", err)
	}

	// Stop the active run: SaveShare now succeeds.
	if err := app.Stop(ctx, store, ownerAccess, opened.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveShare(ctx, ownerAccess, opened.ID, domain.VisibilityPrivate, "", grants, ownerUser); err != nil {
		t.Fatalf("SaveShare after stop = %v", err)
	}

	// Stale running run (older than LiveFor) is failed automatically so sharing is not wedged.
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "stale run message", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		UPDATE chat.runs SET created_at = now() - interval '30 minutes'
		WHERE chat_id = $1::uuid AND state = 'running'`, opened.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveShare(ctx, ownerAccess, opened.ID, domain.VisibilityEveryone, "", grants, ownerUser); err != nil {
		t.Fatalf("SaveShare with stale run = %v", err)
	}
}

func TestShareRowExposesPrivateRead(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}

	row, err := store.ShareRow(ctx, ownerAccess, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.PrivateRead {
		t.Fatalf("fresh chat PrivateRead = true, want false")
	}
	if err := store.NarrowedTx(ctx, scopeOf(ownerAccess, opened.ID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT chat.advance_chat_exposure($1::uuid, false, true)`, opened.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	row, err = store.ShareRow(ctx, ownerAccess, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.PrivateRead {
		t.Fatalf("PrivateRead after private read = false, want true")
	}
}

func TestSaveShareRefusesSharedEditorSameVisibilityAndPreservesGrants(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	const thirdUser = "018f3b2c-7a10-7c4a-8e11-333333333333"
	seedChatUser(t, owner, ownerUser, "Ada", "owner", nil)
	seedChatUser(t, owner, otherUser, "Bob", "member", nil)
	seedChatUser(t, owner, thirdUser, "Cy", "member", nil)

	pool := pgtest.App(t, owner)
	members := identity.Members(pool, nil, "", nil, nil)
	store := quietStore(t, owner)
	store.ReplaceChatGrants = func(ctx context.Context, tx pgx.Tx, chatID string, grants []domain.ShareGrant, grantedBy string) error {
		by, err := identity.ParseUserID(grantedBy)
		if err != nil {
			return err
		}
		writes := make([]identity.GrantWrite, 0, len(grants))
		for _, g := range grants {
			writes = append(writes, identity.GrantWrite{SubjectType: g.SubjectType, SubjectID: g.SubjectID, Level: g.Role})
		}
		return members.ReplaceChatGrantsTx(ctx, tx, chatID, writes, by)
	}

	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	initial := []domain.ShareGrant{
		{SubjectType: domain.SharePerson, SubjectID: otherUser, Role: domain.ShareEditor},
	}
	if err := store.SaveShare(ctx, ownerAccess, opened.ID, domain.VisibilityPrivate, "", initial, ownerUser); err != nil {
		t.Fatalf("owner initial SaveShare = %v", err)
	}

	tampered := []domain.ShareGrant{
		{SubjectType: domain.SharePerson, SubjectID: otherUser, Role: domain.ShareEditor},
		{SubjectType: domain.SharePerson, SubjectID: thirdUser, Role: domain.ShareEditor},
	}
	err = store.SaveShare(ctx, editorOf(opened.ID), opened.ID, domain.VisibilityPrivate, "", tampered, otherUser)
	if !errors.Is(err, app.ErrAccess) {
		t.Fatalf("editor same-visibility SaveShare = %v, want ErrAccess", err)
	}

	rows, err := owner.Query(ctx, `
		SELECT user_id::text, level
		FROM identity.shares
		WHERE object_type = 'chat' AND object_id = $1::uuid
		ORDER BY id`, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type storedRow struct{ userID, level string }
	var got []storedRow
	for rows.Next() {
		var r storedRow
		if err := rows.Scan(&r.userID, &r.level); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].userID != otherUser || got[0].level != domain.ShareEditor {
		t.Fatalf("identity.shares changed after refused SaveShare: got %+v, want [{%s %s}]", got, otherUser, domain.ShareEditor)
	}
}
