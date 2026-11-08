//go:build integration

package notices_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestStoreInsertListRLS(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	migrateIdentity(t, owner)
	if err := notices.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	store := notices.Store{Pool: pool}

	userA := insertUser(t, owner, "ada@example.com")
	userB := insertUser(t, owner, "sam@example.com")

	id, err := store.Insert(ctx, userA, notices.KindProposal, "Weekly close", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id == uuid.Nil {
		t.Fatal("expected id")
	}

	rows, err := store.List(ctx, userA, notices.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != notices.KindProposal || rows[0].Title != "Weekly close" {
		t.Fatalf("user A: %#v", rows)
	}

	other, err := store.List(ctx, userB, notices.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("user B: %#v", other)
	}
}

func TestHTTPNotificationsSession(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	migrateIdentity(t, owner)
	if err := notices.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	userID := insertUser(t, owner, "ada@example.com")
	store := notices.Store{Pool: pool}
	if _, err := store.Insert(ctx, userID, notices.KindProposal, "Mail triage", nil); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	passwords := identity.Passwords(pool, sessions)
	passkeys, err := identity.Passkeys(pool, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		t.Fatal(err)
	}
	id := identity.New(sessions, passkeys, passwords)
	uid, err := identity.ParseUserID(userID.String())
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	notices.Handler{
		List: store,
		User: func(r *http.Request) (uuid.UUID, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return uuid.Nil, false
			}
			return person.UserID.UUID, true
		},
	}.Mount(mux, id.RequireSession)

	anon := httptest.NewRecorder()
	mux.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/api/notifications", nil))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", anon.Code)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
	req.AddCookie(&http.Cookie{
		Name:     "__Host-upmo_session",
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
	})
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with session = %d, want 200", rec.Code)
	}
	var body []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 || body[0]["title"] != "Mail triage" {
		t.Fatalf("body = %s err %v", rec.Body.String(), err)
	}
}

// serve mounts the notification routes for whoever the test says is signed in.
func serve(store notices.Store, user *uuid.UUID) *http.ServeMux {
	mux := http.NewServeMux()
	notices.Handler{
		List:    store,
		Dismiss: store,
		User: func(*http.Request) (uuid.UUID, bool) {
			return *user, *user != uuid.Nil
		},
	}.Mount(mux, nil)
	return mux
}

func call(mux *http.ServeMux, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func listOver(t *testing.T, mux *http.ServeMux, target string) []map[string]any {
	t.Helper()
	rec := call(mux, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", target, rec.Code, rec.Body.String())
	}
	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	return body
}

func setup(t *testing.T) (*pgxpool.Pool, notices.Store) {
	t.Helper()
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	if err := notices.Up(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	return owner, notices.Store{Pool: pgtest.App(t, owner)}
}

func TestListCarriesTheTaskIdOnlyWhenThereIsOne(t *testing.T) {
	owner, store := setup(t)
	ctx := context.Background()
	user := insertUser(t, owner, "ada@example.com")
	taskID := uuid.New()
	if _, err := store.Insert(ctx, user, notices.KindProposal, "Weekly close", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Insert(ctx, user, notices.KindFinished, "Receipts filed", &taskID); err != nil {
		t.Fatal(err)
	}
	mux := serve(store, &user)

	got := listOver(t, mux, "/api/notifications")
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	// Newest first: the finished notice, then the proposal.
	if got[0]["kind"] != notices.KindFinished || got[0]["task_id"] != taskID.String() {
		t.Errorf("finished = %v", got[0])
	}
	if got[1]["kind"] != notices.KindProposal {
		t.Errorf("proposal = %v", got[1])
	}
	if _, has := got[1]["task_id"]; has {
		t.Errorf("a notice with no task carries a task_id key: %v", got[1])
	}
}

func TestListLimit(t *testing.T) {
	owner, store := setup(t)
	user := insertUser(t, owner, "ada@example.com")
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO notices.notifications (user_id, kind, title, created_at)
		SELECT $1, 'question', 'Question ' || n, now() - make_interval(secs => n)
		FROM generate_series(1, 105) AS n`, user); err != nil {
		t.Fatal(err)
	}
	mux := serve(store, &user)

	tests := []struct {
		query string
		want  int
	}{
		{"", 50},
		{"?limit=3", 3},
		{"?limit=100", 100},
		{"?limit=101", 100},
		{"?limit=100000", 100},
	}
	for _, test := range tests {
		if got := listOver(t, mux, "/api/notifications"+test.query); len(got) != test.want {
			t.Errorf("%q: %d notices, want %d", test.query, len(got), test.want)
		}
	}
	first := listOver(t, mux, "/api/notifications?limit=2")
	if first[0]["title"] != "Question 1" || first[1]["title"] != "Question 2" {
		t.Errorf("not newest first: %v", first)
	}
	for _, query := range []string{"?limit=0", "?limit=-1", "?limit=ten", "?limit="} {
		if rec := call(mux, http.MethodGet, "/api/notifications"+query); rec.Code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", query, rec.Code)
		}
	}
}

func TestDismissHidesTheNoticeAndIsIdempotent(t *testing.T) {
	owner, store := setup(t)
	ctx := context.Background()
	user := insertUser(t, owner, "ada@example.com")
	keep, err := store.Insert(ctx, user, notices.KindQuestion, "Keep me", nil)
	if err != nil {
		t.Fatal(err)
	}
	drop, err := store.Insert(ctx, user, notices.KindProposal, "Drop me", nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := serve(store, &user)

	rec := call(mux, http.MethodDelete, "/api/notifications/"+drop.String())
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("dismiss = %d %q", rec.Code, rec.Body.String())
	}
	got := listOver(t, mux, "/api/notifications")
	if len(got) != 1 || got[0]["id"] != keep.String() {
		t.Fatalf("after dismiss: %v", got)
	}
	var first time.Time
	if err := owner.QueryRow(ctx, `SELECT dismissed_at FROM notices.notifications WHERE id = $1`, drop).Scan(&first); err != nil {
		t.Fatal(err)
	}

	// Dismissing twice is 204 again and keeps the first time.
	if rec := call(mux, http.MethodDelete, "/api/notifications/"+drop.String()); rec.Code != http.StatusNoContent {
		t.Fatalf("second dismiss = %d", rec.Code)
	}
	var again time.Time
	if err := owner.QueryRow(ctx, `SELECT dismissed_at FROM notices.notifications WHERE id = $1`, drop).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if !again.Equal(first) {
		t.Errorf("dismissed_at moved from %v to %v", first, again)
	}
	var rows int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM notices.notifications`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("rows = %d %v, a dismissed notice stays stored", rows, err)
	}
}

func TestDismissRefusesAnotherPersonsNoticeAndAnUnknownOne(t *testing.T) {
	owner, store := setup(t)
	ctx := context.Background()
	ada := insertUser(t, owner, "ada@example.com")
	sam := insertUser(t, owner, "sam@example.com")
	adas, err := store.Insert(ctx, ada, notices.KindQuestion, "Ada's", nil)
	if err != nil {
		t.Fatal(err)
	}
	asSam := serve(store, &sam)

	for _, id := range []string{adas.String(), uuid.NewString(), "not-an-id"} {
		if rec := call(asSam, http.MethodDelete, "/api/notifications/"+id); rec.Code != http.StatusNotFound {
			t.Errorf("sam dismisses %s = %d, want 404", id, rec.Code)
		}
	}
	if got := listOver(t, serve(store, &ada), "/api/notifications"); len(got) != 1 {
		t.Errorf("ada lost her notice: %v", got)
	}
	var none uuid.UUID
	if rec := call(serve(store, &none), http.MethodDelete, "/api/notifications/"+adas.String()); rec.Code != http.StatusUnauthorized {
		t.Errorf("no person = %d, want 401", rec.Code)
	}
}

func TestAppRoleMayOnlySetDismissedAtOnOwnRows(t *testing.T) {
	owner, store := setup(t)
	ctx := context.Background()
	ada := insertUser(t, owner, "ada@example.com")
	sam := insertUser(t, owner, "sam@example.com")
	id, err := store.Insert(ctx, ada, notices.KindQuestion, "Ada's", nil)
	if err != nil {
		t.Fatal(err)
	}
	pool := store.Pool

	as := func(user uuid.UUID, statement string, args ...any) (int64, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, user.String()); err != nil {
			t.Fatal(err)
		}
		tag, err := tx.Exec(ctx, statement, args...)
		return tag.RowsAffected(), err
	}
	for _, column := range []string{"title", "kind", "user_id", "task_id", "created_at"} {
		statement := fmt.Sprintf(`UPDATE notices.notifications SET %[1]s = %[1]s`, column)
		if _, err := as(ada, statement); err == nil {
			t.Errorf("the app role updated %s", column)
		}
	}
	if _, err := as(ada, `DELETE FROM notices.notifications`); err == nil {
		t.Error("the app role deleted a notice")
	}
	if n, err := as(sam, `UPDATE notices.notifications SET dismissed_at = now() WHERE id = $1`, id); err != nil || n != 0 {
		t.Errorf("sam changed ada's notice: %d rows, %v", n, err)
	}
	if n, err := as(ada, `UPDATE notices.notifications SET dismissed_at = now() WHERE id = $1`, id); err != nil || n != 1 {
		t.Errorf("ada dismissing her own: %d rows, %v", n, err)
	}
}

func migrateIdentity(t *testing.T, owner *pgxpool.Pool) {
	t.Helper()
	if err := migrate.Up(context.Background(), owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
}

func insertUser(t *testing.T, owner *pgxpool.Pool, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := owner.QueryRow(context.Background(), `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, $2, (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id`, email, email).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
