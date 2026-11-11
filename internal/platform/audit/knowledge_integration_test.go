//go:build integration

package audit_test

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// knowledgeRig migrates audit, identity and knowledge on a fresh database and
// returns the owner pool and the request pool that runs as upmo_app.
func knowledgeRig(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, knowledge.Schema, knowledge.Migrations()); err != nil {
		t.Fatal(err)
	}
	return owner, pgtest.App(t, owner)
}

var knowledgeAccess = app.Access{
	UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
	Allow:  "folder:create,folder:view,folder:edit,folder:delete",
}

func TestKnowledgeFolderActionsSealTheirWrites(t *testing.T) {
	owner, appPool := knowledgeRig(t)
	sealed := newSealer(t, owner)
	store := knowledge.NewStore(appPool, nil)
	ctx := context.Background()

	id, err := store.CreateFolder(ctx, knowledgeAccess, "the hidden folder")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RenameFolder(ctx, knowledgeAccess, id, "the renamed folder"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteFolder(ctx, knowledgeAccess, id); err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 3 {
		t.Fatalf("%d events, want create, rename and delete", len(rows))
	}
	if rows[0].Action != audit.FolderCreate || rows[0].TargetID != id {
		t.Fatalf("first event = %s target %s, want the created folder %s", rows[0].Action, rows[0].TargetID, id)
	}
	if hasAnyValue(rows[0]) {
		t.Fatalf("create changes = %s, want no values", changesJSON(t, rows[0]))
	}
	if rows[1].Action != audit.FolderUpdate || rows[1].TargetID != id {
		t.Fatalf("second event = %s target %s, want the renamed folder", rows[1].Action, rows[1].TargetID)
	}
	// A folder name is content: the event says a name changed, and no more.
	if strings.Join(rows[1].Changes.Fields, ",") != "name" || hasAnyValue(rows[1]) {
		t.Fatalf("rename changes = %s, want the field name alone", changesJSON(t, rows[1]))
	}
	if containsSecret(rows[1], "the renamed folder") {
		t.Fatalf("rename changes hold the folder name: %s", changesJSON(t, rows[1]))
	}
	if rows[2].Action != audit.FolderDelete || rows[2].TargetID != id {
		t.Fatalf("third event = %s target %s, want the deleted folder", rows[2].Action, rows[2].TargetID)
	}
}

func TestKnowledgeSourceActionsSealTheirWrites(t *testing.T) {
	owner, appPool := knowledgeRig(t)
	sealed := newSealer(t, owner)
	store := knowledge.NewStore(appPool, nil)
	ctx := context.Background()

	folder, err := store.CreateFolder(ctx, knowledgeAccess, "receipts")
	if err != nil {
		t.Fatal(err)
	}
	sealed()

	// A URL source waits on its fetch as a pending row.
	pending, err := store.CreatePending(ctx, knowledgeAccess, app.Pending{
		FolderID:   folder,
		Title:      "A pending page",
		Kind:       "url",
		Provenance: "curated",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.SourceAdd {
		t.Fatalf("events = %v, want one source.add", actions(rows))
	}
	if rows[0].TargetID != pending {
		t.Fatalf("target = %s, want the pending source %s", rows[0].TargetID, pending)
	}
	if got := changeOf(t, rows[0], "kind"); got.To != "url" {
		t.Fatalf("kind = %v, want url", got.To)
	}

	// A direct markdown draft publishes a source in one transaction.
	hash := sha256.Sum256([]byte("the confidential manual"))
	var saved app.Result
	if err := store.InTx(ctx, knowledgeAccess, func(tx app.Tx) error {
		result, err := tx.Save(ctx, app.Draft{FolderID: folder, Title: "Manual", Body: "the confidential manual", Kind: "markdown"}, hash, nil)
		saved = result
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.SourceAdd {
		t.Fatalf("events = %v, want the published source's source.add", actions(rows))
	}
	if rows[0].TargetID != saved.ID {
		t.Fatalf("target = %s, want the saved source %s", rows[0].TargetID, saved.ID)
	}
	if got := changeOf(t, rows[0], "kind"); got.To != "markdown" {
		t.Fatalf("kind = %v, want markdown", got.To)
	}
	if got := changeOf(t, rows[0], "revision"); got.To != float64(1) {
		t.Fatalf("revision = %v, want the first revision", got.To)
	}
	if containsSecret(rows[0], "the confidential manual") {
		t.Fatalf("source.add changes hold the body: %s", changesJSON(t, rows[0]))
	}

	// A reingest resets a ready source; its event carries the status edge.
	if err := store.ResetPending(ctx, knowledgeAccess, saved.ID); err != nil {
		t.Fatal(err)
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.SourceReingest {
		t.Fatalf("events = %v, want one source.reingest", actions(rows))
	}
	if from, to := reingestEdge(t, rows[0]); from != "ready" || to != "pending" {
		t.Fatalf("status = %s -> %s, want the reset", from, to)
	}

	if _, err := store.DeleteSource(ctx, knowledgeAccess, folder, pending); err != nil {
		t.Fatal(err)
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.SourceDelete {
		t.Fatalf("events = %v, want one source.delete", actions(rows))
	}
	if rows[0].TargetID != pending {
		t.Fatalf("target = %s, want the deleted source %s", rows[0].TargetID, pending)
	}
}

func TestKnowledgeRouteSealsWithThePersonActor(t *testing.T) {
	owner, appPool := knowledgeRig(t)
	sealed := newSealer(t, owner)
	store := knowledge.NewStore(appPool, nil)

	mux := http.NewServeMux()
	knowledge.Handler{
		Store: store,
		Access: func(r *http.Request) (app.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: knowledgeAccess.UserID, CredentialType: "session"})
			return knowledgeAccess, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/knowledge/folders", strings.NewReader(`{"name":"the hidden folder"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.FolderCreate {
		t.Fatalf("events = %v, want one folder.create", actions(rows))
	}
	row := rows[0]
	// The adapter recorded the event, so the middleware left no fallback.
	if row.Reason != "" {
		t.Fatalf("reason = %q, want the handler's own event", row.Reason)
	}
	if row.ActorType != "user" || row.ActorID != knowledgeAccess.UserID || row.CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.CredentialType)
	}
	if containsSecret(row, "the hidden folder") {
		t.Fatalf("event holds the folder name: %s", changesJSON(t, row))
	}
}

func TestKnowledgeRouteRefusalRecordsDenied(t *testing.T) {
	owner, appPool := knowledgeRig(t)
	sealed := newSealer(t, owner)
	store := knowledge.NewStore(appPool, nil)

	stranger := app.Access{UserID: "018f3b2c-7a10-7c4a-8e11-444444444444"}
	mux := http.NewServeMux()
	knowledge.Handler{
		Store: store,
		Access: func(r *http.Request) (app.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: stranger.UserID, CredentialType: "session"})
			// Signed in, but no folder permission: the handler refuses.
			return stranger, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/knowledge/folders", strings.NewReader(`{"name":"no such folder"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a person without folder permission = %d %s, want 403", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.FolderCreate {
		t.Fatalf("events = %v, want one denied folder.create", actions(rows))
	}
	if rows[0].Outcome != audit.Denied || rows[0].ActorID != stranger.UserID {
		t.Fatalf("event = %s %s %s, want the refused person denied", rows[0].Action, rows[0].Outcome, rows[0].ActorID)
	}
	if containsSecret(rows[0], "no such folder") {
		t.Fatalf("denied event holds the refused name: %s", changesJSON(t, rows[0]))
	}
}

// reingestEdge reads the status a reingest reset.
func reingestEdge(t *testing.T, row audit.Row) (string, string) {
	t.Helper()
	change := changeOf(t, row, "status")
	from, _ := change.From.(string)
	to, _ := change.To.(string)
	return from, to
}
