//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/app"
)

func TestReadReportsRenameAndDeletePermissions(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)
	insertUser(t, owner, ownerUser, "permissions-owner@example.com", "owner")
	insertUser(t, owner, otherUser, "permissions-editor@example.com", "member")
	const viewerUser = "018f3b2c-7a10-7c4a-8e11-333333333333"
	insertUser(t, owner, viewerUser, "permissions-viewer@example.com", "member")

	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []struct{ user, level string }{{otherUser, "editor"}, {viewerUser, "viewer"}} {
		if _, err := owner.Exec(ctx, `
			INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
			VALUES ('chat', $1::uuid, $2::uuid, $3, $4::uuid)`, opened.ID, grant.user, grant.level, ownerUser); err != nil {
			t.Fatal(err)
		}
	}

	viewer := app.Access{UserID: viewerUser, Allow: "chat:view,chat:edit", SharedView: "{" + opened.ID + "}"}
	got, err := app.Read(ctx, store, viewer, opened.ID)
	if err != nil || got.CanEdit || got.CanDelete {
		t.Fatalf("viewer permissions = edit:%v delete:%v err:%v", got.CanEdit, got.CanDelete, err)
	}

	editor := app.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: "{" + opened.ID + "}", SharedEdit: "{" + opened.ID + "}"}
	got, err = app.Read(ctx, store, editor, opened.ID)
	if err != nil || !got.CanEdit || got.CanDelete {
		t.Fatalf("editor permissions = edit:%v delete:%v err:%v", got.CanEdit, got.CanDelete, err)
	}
	t.Run("shared editor with chat:delete permission cannot delete", func(t *testing.T) {
		editorWithDelete := editor
		editorWithDelete.Allow += ",chat:delete"
		got, err := app.Read(ctx, store, editorWithDelete, opened.ID)
		if err != nil || !got.CanEdit || got.CanDelete {
			t.Fatalf("editor with delete permission = edit:%v delete:%v err:%v", got.CanEdit, got.CanDelete, err)
		}
	})

	ownerAccessWithDelete := ownerAccess
	ownerAccessWithDelete.Allow += ",chat:delete"
	got, err = app.Read(ctx, store, ownerAccessWithDelete, opened.ID)
	if err != nil || !got.CanEdit || !got.CanDelete {
		t.Fatalf("owner permissions = edit:%v delete:%v err:%v", got.CanEdit, got.CanDelete, err)
	}
}

func TestRenameReturnsLockRefusalWhenUpdateAffectsNoChat(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		CREATE FUNCTION chat.skip_rename_test() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;
		CREATE TRIGGER chats_skip_rename_test BEFORE UPDATE OF title ON chat.chats
		FOR EACH ROW EXECUTE FUNCTION chat.skip_rename_test()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `DROP TRIGGER IF EXISTS chats_skip_rename_test ON chat.chats`)
		_, _ = owner.Exec(ctx, `DROP FUNCTION IF EXISTS chat.skip_rename_test()`)
	})

	var before int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.pending WHERE action = 'chat.update' AND target_id = $1`, opened.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Rename(ctx, store, ownerAccess, opened.ID, "Suppressed"); !errors.Is(err, app.ErrSay) {
		t.Fatalf("rename error = %v, want ErrSay", err)
	}
	var after int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.pending WHERE action = 'chat.update' AND target_id = $1`, opened.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("rename audit events = %d, want unchanged at %d", after, before)
	}
}
