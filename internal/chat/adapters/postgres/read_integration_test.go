//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestListKeepsTheNewestHundredInActivityOrder(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.chats (id, owner_user_id, visibility)
		SELECT ('018f3b2c-7a10-7c4a-8e11-30000000' || lpad(n::text, 4, '0'))::uuid, $1::uuid, 'private'
		FROM generate_series(1, 101) AS n`, ownerUser); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, body, at)
		SELECT ('018f3b2c-7a10-7c4a-8e11-30000000' || lpad(n::text, 4, '0'))::uuid, 1, 'chat ' || n,
		       CASE WHEN n = 50 THEN now()
		            ELSE now() - (102 - n) * interval '1 minute' END
		FROM generate_series(1, 101) AS n`); err != nil {
		t.Fatal(err)
	}

	listed, err := app.List(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 100 {
		t.Fatalf("listed %d chats", len(listed))
	}
	// Chat 50 was written last, so it leads even though its id sorts low, and
	// the oldest chat drops off the end of the list.
	if listed[0].Title != "chat 50" || listed[0].Preview != "chat 50" || listed[1].Title != "chat 101" {
		t.Fatalf("first two = %+v %+v", listed[0], listed[1])
	}
	for i := 1; i < len(listed); i++ {
		if listed[i].LastActivity.After(listed[i-1].LastActivity) {
			t.Fatalf("chat %d is newer than the one before it", i)
		}
	}
	titles := map[string]bool{}
	for _, summary := range listed {
		titles[summary.Title] = true
	}
	if titles["chat 1"] || !titles["chat 2"] {
		t.Fatalf("oldest kept: chat 1 listed %t, chat 2 listed %t", titles["chat 1"], titles["chat 2"])
	}
}

func TestListDerivesTitleAndPreview(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()

	blank, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, flat.ID, "  Plan   the\n\tweek  ", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	cut, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, cut.ID, strings.Repeat("a", domain.TitleRunes+1), app.Picks{}); err != nil {
		t.Fatal(err)
	}
	long, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, long.ID, strings.Repeat("b", domain.PreviewRunes+5), app.Picks{}); err != nil {
		t.Fatal(err)
	}
	answered, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, answered.ID, "first words", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	settle(t, owner, answered.ID)
	if err := app.Say(ctx, store, ownerAccess, answered.ID, "later words", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, tokens)
		VALUES ($1::uuid, 3, 'assistant', 'The answer is 42', 'claude-opus-5-5', 20)`, answered.ID); err != nil {
		t.Fatal(err)
	}
	var standing string
	if err := owner.QueryRow(ctx, `
		INSERT INTO chat.chats (owner_user_id, visibility)
		VALUES ($1::uuid, 'private')
		RETURNING id::text`, ownerUser).Scan(&standing); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, tokens)
		VALUES ($1::uuid, 1, 'assistant', 'Standing by', 'claude-opus-5-5', 0)`, standing); err != nil {
		t.Fatal(err)
	}

	listed, err := app.List(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]app.ChatSummary{
		blank.ID:    {ID: blank.ID, Title: "New chat", Preview: ""},
		flat.ID:     {ID: flat.ID, Title: "Plan the week", Preview: "Plan the week"},
		cut.ID:      {ID: cut.ID, Title: strings.Repeat("a", domain.TitleRunes), Preview: strings.Repeat("a", domain.TitleRunes+1)},
		long.ID:     {ID: long.ID, Title: strings.Repeat("b", domain.TitleRunes), Preview: strings.Repeat("b", domain.PreviewRunes)},
		answered.ID: {ID: answered.ID, Title: "first words", Preview: "The answer is 42"},
		standing:    {ID: standing, Title: "New chat", Preview: "Standing by"},
	}
	got := map[string]app.ChatSummary{}
	for _, summary := range listed {
		got[summary.ID] = summary
	}
	for id, summary := range want {
		summary.LastActivity = got[id].LastActivity
		if !reflect.DeepEqual(got[id], summary) {
			t.Fatalf("chat %s = %+v, want %+v", id, got[id], summary)
		}
		if got[id].LastActivity.IsZero() {
			t.Fatalf("chat %s has no last activity", id)
		}
	}
}

func TestListFallsBackToTheChatCreationTime(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	said, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, said.ID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	var empty string
	if err := owner.QueryRow(ctx, `
		INSERT INTO chat.chats (owner_user_id, visibility, created_at)
		VALUES ($1::uuid, 'private', now() - interval '1 hour')
		RETURNING id::text`, ownerUser).Scan(&empty); err != nil {
		t.Fatal(err)
	}

	listed, err := app.List(ctx, store, ownerAccess)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list = %+v %v", listed, err)
	}
	// The said chat's line is newer than the empty chat's creation.
	if listed[0].ID != said.ID || listed[1].ID != empty {
		t.Fatalf("order = %s, %s", listed[0].ID, listed[1].ID)
	}
	if listed[1].Title != "New chat" || listed[1].Preview != "" {
		t.Fatalf("empty chat = %+v", listed[1])
	}
	var created time.Time
	if err := owner.QueryRow(ctx, `SELECT created_at FROM chat.chats WHERE id = $1`, empty).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if !listed[1].LastActivity.Equal(created) {
		t.Fatalf("last activity = %v, want the creation %v", listed[1].LastActivity, created)
	}
}

func TestSharingLetsAViewerReadAndAnEditorWrite(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	viewer := app.Access{UserID: otherUser, Allow: "chat:view", SharedView: "{" + opened.ID + "}"}
	editor := app.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: "{" + opened.ID + "}", SharedEdit: "{" + opened.ID + "}"}

	listed, err := app.List(ctx, store, viewer)
	if err != nil || len(listed) != 1 || listed[0].ID != opened.ID || listed[0].Preview != "hello" {
		t.Fatalf("viewer list = %+v %v", listed, err)
	}
	got, err := app.Read(ctx, store, viewer, opened.ID)
	if err != nil || len(got.Lines) != 1 {
		t.Fatalf("viewer read = %+v %v", got, err)
	}
	if err := app.Say(ctx, store, viewer, opened.ID, "hi", app.Picks{}); !errors.Is(err, app.ErrSay) {
		t.Fatalf("viewer say = %v", err)
	}

	settle(t, owner, opened.ID)
	if err := app.Say(ctx, store, editor, opened.ID, "hi there", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	got, err = app.Read(ctx, store, viewer, opened.ID)
	if err != nil || len(got.Lines) != 2 || got.Lines[1].Body != "hi there" {
		t.Fatalf("after the editor said = %+v %v", got, err)
	}

	listed, err = app.List(ctx, store, otherAccess)
	if err != nil || len(listed) != 0 {
		t.Fatalf("stranger list = %+v %v", listed, err)
	}
	if _, err := app.Read(ctx, store, otherAccess, opened.ID); !errors.Is(err, app.ErrGone) {
		t.Fatalf("stranger read = %v", err)
	}
	if err := app.Say(ctx, store, otherAccess, opened.ID, "no", app.Picks{}); !errors.Is(err, app.ErrGone) {
		t.Fatalf("stranger say = %v", err)
	}
}

func TestRecentLinesReadsASharedChatForAnEditorAndNotAStranger(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	editor := app.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: "{" + opened.ID + "}", SharedEdit: "{" + opened.ID + "}"}
	settle(t, owner, opened.ID)
	if err := app.Say(ctx, store, editor, opened.ID, "hi there", app.Picks{}); err != nil {
		t.Fatal(err)
	}

	got, err := store.RecentLines(ctx, editor, opened.ID, 10)
	if err != nil || !reflect.DeepEqual(got, []string{"hello", "hi there"}) {
		t.Fatalf("editor lines = %v %v", got, err)
	}
	if _, err := store.RecentLines(ctx, otherAccess, opened.ID, 10); !errors.Is(err, app.ErrGone) {
		t.Fatalf("stranger lines = %v", err)
	}
}

func TestReadTypesTheLines(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, tokens, cost_micro_usd)
		VALUES
		  ($1::uuid, 2, 'assistant', 'First reply', 'claude-opus-5-5', 184, 2000),
		  ($1::uuid, 3, 'assistant', 'Second reply', 'glm-5.3', 37, NULL)`, opened.ID); err != nil {
		t.Fatal(err)
	}

	got, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 3 {
		t.Fatalf("lines = %+v", got.Lines)
	}
	user := got.Lines[0]
	if user.Role != "user" || user.Body != "hello" || user.Model != nil || user.Tokens != 0 || user.CostMicroUSD != nil {
		t.Fatalf("user line = %+v", user)
	}
	first := got.Lines[1]
	if first.Role != "assistant" || *first.Model != "claude-opus-5-5" || first.Tokens != 184 || first.CostMicroUSD == nil || *first.CostMicroUSD != 2000 {
		t.Fatalf("priced line = %+v", first)
	}
	second := got.Lines[2]
	if second.Role != "assistant" || *second.Model != "glm-5.3" || second.Tokens != 37 || second.CostMicroUSD != nil {
		t.Fatalf("unpriced line = %+v", second)
	}
	for i, line := range got.Lines {
		if line.ID == "" || line.At.IsZero() || (i > 0 && line.At.Before(got.Lines[i-1].At)) {
			t.Fatalf("line %d = %+v", i, line)
		}
	}

	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body)
		VALUES ($1::uuid, 4, 'assistant', 'no model')`, opened.ID); err == nil {
		t.Fatal("an assistant line was stored without a model")
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, cost_micro_usd)
		VALUES ($1::uuid, 5, 'user', 'priced user', 1)`, opened.ID); err == nil {
		t.Fatal("a user line was stored with a cost")
	}
}

func TestHTTPServesTheChats(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	serve := func(access app.Access, ok bool) http.Handler {
		mux := http.NewServeMux()
		chat.Handler{
			Book:   store,
			Access: func(*http.Request) (app.Access, bool) { return access, ok },
		}.Mount(mux, nil)
		return mux
	}
	do := func(h http.Handler, method, path, body string) (int, []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code, rec.Body.Bytes()
	}

	owned := serve(ownerAccess, true)
	code, body := do(owned, http.MethodPost, "/api/chats", "")
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var created struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Preview      string `json:"preview"`
		LastActivity string `json:"last_activity"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Title != "New chat" || created.Preview != "" {
		t.Fatalf("created = %+v", created)
	}
	if _, err := time.Parse(time.RFC3339, created.LastActivity); err != nil {
		t.Fatalf("last activity = %q", created.LastActivity)
	}
	path := "/api/chats/" + created.ID

	code, body = do(owned, http.MethodPost, path, `{"body":" hi "}`)
	if code != http.StatusAccepted || len(body) != 0 {
		t.Fatalf("say = %d %q", code, body)
	}

	code, body = do(owned, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("get = %d %s", code, body)
	}
	var read struct {
		ID    string
		Lines []struct {
			ID      string
			Role    string
			Body    string
			Model   *string
			Tokens  int
			CostUSD *float64 `json:"cost_usd"`
			At      string
		}
	}
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatal(err)
	}
	if read.ID != created.ID || len(read.Lines) != 1 {
		t.Fatalf("read = %+v", read)
	}
	line := read.Lines[0]
	if line.ID == "" || line.Role != "user" || line.Body != "hi" || line.Model != nil || line.Tokens != 0 || line.CostUSD != nil {
		t.Fatalf("line = %+v", line)
	}
	if _, err := time.Parse(time.RFC3339, line.At); err != nil {
		t.Fatalf("at = %q", line.At)
	}

	_, body = do(owned, http.MethodPost, "/api/chats", "")
	var second struct{ ID string }
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	if code, body := do(owned, http.MethodPost, "/api/chats/"+second.ID, `{"body":"second"}`); code != http.StatusAccepted {
		t.Fatalf("second say = %d %s", code, body)
	}
	code, body = do(owned, http.MethodGet, "/api/chats", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	var listed []struct{ ID, Title, Preview string }
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != second.ID || listed[1].ID != created.ID || listed[0].Preview != "second" {
		t.Fatalf("list = %+v", listed)
	}

	stranger := serve(otherAccess, true)
	code, body = do(stranger, http.MethodGet, path, "")
	if code != http.StatusNotFound || strings.TrimSpace(string(body)) != `{"error":"no such chat"}` {
		t.Fatalf("stranger get = %d %s", code, body)
	}
	if code, body := do(stranger, http.MethodPost, path, `{"body":"no"}`); code != http.StatusNotFound {
		t.Fatalf("stranger say = %d %s", code, body)
	}
	if code, body := do(stranger, http.MethodGet, "/api/chats", ""); code != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("stranger list = %d %s", code, body)
	}
	if code, body := do(owned, http.MethodGet, "/api/chats/018f3b2c-7a10-7c4a-8e11-ffffffffffff", ""); code != http.StatusNotFound {
		t.Fatalf("unknown chat = %d %s", code, body)
	}

	share := "{" + created.ID + "}"
	viewer := serve(app.Access{UserID: otherUser, Allow: "chat:view", SharedView: share}, true)
	code, body = do(viewer, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("viewer get = %d %s", code, body)
	}
	if code, body := do(viewer, http.MethodPost, path, `{"body":"no"}`); code != http.StatusForbidden {
		t.Fatalf("viewer say = %d %s", code, body)
	}
	editor := serve(app.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: share, SharedEdit: share}, true)
	settle(t, owner, created.ID)
	if code, body := do(editor, http.MethodPost, path, `{"body":"yes"}`); code != http.StatusAccepted {
		t.Fatalf("editor say = %d %s", code, body)
	}

	if code, body := do(owned, http.MethodPost, path, `{"body":`); code != http.StatusBadRequest {
		t.Fatalf("broken json = %d %s", code, body)
	}
	if code, body := do(owned, http.MethodPost, path, `{}`); code != http.StatusBadRequest {
		t.Fatalf("missing body = %d %s", code, body)
	}
	if code, body := do(owned, http.MethodPost, path, `{"body":"   "}`); code != http.StatusBadRequest {
		t.Fatalf("blank line = %d %s", code, body)
	}
	long := `{"body":"` + strings.Repeat("a", domain.LineRunes+1) + `"}`
	if code, body := do(owned, http.MethodPost, path, long); code != http.StatusBadRequest {
		t.Fatalf("long line = %d %s", code, body)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	owned.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bearer = %d %s", rec.Code, rec.Body.String())
	}
	if code, body := do(serve(app.Access{}, false), http.MethodGet, "/api/chats", ""); code != http.StatusUnauthorized {
		t.Fatalf("no session = %d %s", code, body)
	}
}

// recorder is an Enqueuer that keeps the kind of every job it is given.
type recorder struct{ kinds []string }

func (r *recorder) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	r.kinds = append(r.kinds, args.Kind())
	return &rivertype.JobInsertResult{}, nil
}

func TestSayStillEnqueuesTheScoutAndTheExtract(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	pool := pgtest.App(t, owner)
	jobsSeen := &recorder{}
	store := postgres.Store{Pool: pool, Enqueuer: jobsSeen}
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}

	got := append([]string(nil), jobsSeen.kinds...)
	sort.Strings(got)
	want := []string{"memory_extract", "tasks_proposal_scout"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enqueued = %v, want %v", got, want)
	}
}

func TestFinalSchemaLineDefaultsOnInsert(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	var chatID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat.chats (owner_user_id, visibility)
		VALUES ($1::uuid, 'private')
		RETURNING id::text`, ownerUser).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	for i, body := range []string{"first line", "second line"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.lines (chat_id, seq, body)
			VALUES ($1::uuid, $2, $3)`, chatID, i+1, body); err != nil {
			t.Fatal(err)
		}
	}

	store := postgres.Store{Pool: pgtest.App(t, pool)}
	got, err := app.Read(ctx, store, ownerAccess, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 || got.Lines[0].Body != "first line" || got.Lines[1].Body != "second line" {
		t.Fatalf("lines = %+v", got.Lines)
	}
	first, second := got.Lines[0], got.Lines[1]
	if first.ID == "" || second.ID == "" || first.ID == second.ID {
		t.Fatalf("line ids = %q %q", first.ID, second.ID)
	}
	for _, line := range got.Lines {
		if line.Role != "user" || line.Model != nil || line.Tokens != 0 || line.CostMicroUSD != nil || line.Status != "done" {
			t.Fatalf("default line = %+v", line)
		}
	}
	var feed1, feed2 string
	if err := pool.QueryRow(ctx, `SELECT feed_id::text FROM chat.lines WHERE chat_id = $1 AND seq = 1`, chatID).Scan(&feed1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT feed_id::text FROM chat.lines WHERE chat_id = $1 AND seq = 2`, chatID).Scan(&feed2); err != nil {
		t.Fatal(err)
	}
	if feed1 == "" || feed2 == "" || feed1 == feed2 {
		t.Fatalf("feed ids invalid: %q %q", feed1, feed2)
	}
	var created time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM chat.chats WHERE id = $1`, chatID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created.IsZero() || first.At.IsZero() {
		t.Fatalf("created=%v at=%v", created, first.At)
	}

	listed, err := app.List(ctx, store, ownerAccess)
	if err != nil || len(listed) != 1 || listed[0].Title != "first line" || listed[0].Preview != "second line" {
		t.Fatalf("list = %+v %v", listed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE chat.lines SET body = 'changed' WHERE chat_id = $1`, chatID); err == nil {
		t.Fatal("a line was rewritten")
	}
}

func TestVisible_UPM252_AnswersForTheOwnerAndASharedViewerAndNoOneElse(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	viewer := app.Access{UserID: otherUser, Allow: "chat:view", SharedView: "{" + opened.ID + "}"}

	for name, access := range map[string]app.Access{"owner": ownerAccess, "shared viewer": viewer} {
		if err := store.Visible(ctx, access, opened.ID); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := store.Visible(ctx, otherAccess, opened.ID); !errors.Is(err, app.ErrGone) {
		t.Fatalf("stranger = %v", err)
	}
	if err := store.Visible(ctx, ownerAccess, "018f3b2c-7a10-7c4a-8e11-00000000dead"); !errors.Is(err, app.ErrGone) {
		t.Fatalf("no such chat = %v", err)
	}
}

func TestRead_UPM251_PendingMarksAUserLineWithNoStoredReply(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"answered", "failed", "waiting"} {
		if err := app.Say(ctx, store, ownerAccess, opened.ID, body, app.Picks{}); err != nil {
			t.Fatal(err)
		}
		settle(t, owner, opened.ID)
	}
	// Given a done reply to the first line and a failed reply to the second,
	// and none to the third.
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, status, reply_to)
		SELECT chat_id, seq + 10, 'assistant', CASE body WHEN 'answered' THEN 'Here you go' ELSE '' END,
		       CASE body WHEN 'answered' THEN 'claude-opus-5-5' ELSE '' END,
		       CASE body WHEN 'answered' THEN 'done' ELSE 'failed' END, id
		FROM chat.lines
		WHERE chat_id = $1::uuid AND role = 'user' AND body IN ('answered', 'failed')`, opened.ID); err != nil {
		t.Fatal(err)
	}

	// When the thread is read.
	got, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Then only the user line with no reply is pending.
	pending := map[string]bool{}
	for _, line := range got.Lines {
		if line.Role == "user" {
			pending[line.Body] = line.Pending
		} else if line.Pending {
			t.Fatalf("assistant line %q is pending", line.Body)
		}
	}
	want := map[string]bool{"answered": false, "failed": false, "waiting": true}
	if !reflect.DeepEqual(pending, want) {
		t.Fatalf("pending = %v, want %v", pending, want)
	}
}
