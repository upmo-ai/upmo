//go:build integration

package brain

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/artifacts"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestBrain_UPM222_ListDownloadTextDiffAndAccess(t *testing.T) {
	ctx := context.Background()
	owner := pgtest.New(t)
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, artifacts.Schema, artifacts.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)

	var ownerID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		SELECT 'owner@example.com', 'Owner', id, now() FROM identity.roles WHERE key = 'owner'
		RETURNING id::text`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	var memberID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		SELECT 'member@example.com', 'Member', id, now() FROM identity.roles WHERE key = 'member'
		RETURNING id::text`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	var lockedRole string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.roles (name, permissions) VALUES ('No artifact view', ARRAY[]::text[])
		RETURNING id::text`).Scan(&lockedRole); err != nil {
		t.Fatal(err)
	}
	var lockedID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('locked@example.com', 'Locked', $1::uuid, now())
		RETURNING id::text`, lockedRole).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
		ArtifactsDir:  t.TempDir(),
	}
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	ownerToken := startSession(t, ctx, sessions, ownerID)
	memberToken := startSession(t, ctx, sessions, memberID)
	lockedToken := startSession(t, ctx, sessions, lockedID)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	work, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, path string, bearer bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "__Host-upmo_session", Value: token, Path: "/", Secure: true, HttpOnly: true})
		}
		if bearer {
			req.Header.Set("Authorization", "Bearer upmo_pat_secret")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	openReq := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"goal":"artifact notes"}`))
	openReq.AddCookie(&http.Cookie{Name: "__Host-upmo_session", Value: ownerToken, Path: "/", Secure: true, HttpOnly: true})
	opened := httptest.NewRecorder()
	handler.ServeHTTP(opened, openReq)
	if opened.Code != http.StatusCreated {
		t.Fatalf("open = %d %s", opened.Code, opened.Body.String())
	}
	var openedBody map[string]string
	if err := json.Unmarshal(opened.Body.Bytes(), &openedBody); err != nil {
		t.Fatal(err)
	}
	taskID := openedBody["id"]
	if taskID == "" {
		t.Fatalf("open body %s", opened.Body.String())
	}

	vol := artifacts.Volume{Root: cfg.ArtifactsDir}
	store := artifacts.Store{Pool: pool}
	create := artifacts.Access{UserID: ownerID, Allow: "artifact:create,artifact:view"}
	put := func(name, kind string, body []byte, size int64) string {
		t.Helper()
		if err := vol.Put(ctx, name, body); err != nil {
			t.Fatal(err)
		}
		if size < 0 {
			size = int64(len(body))
		}
		id, err := store.Put(ctx, create, artifacts.KeepRecord{
			Owner: ownerID, Task: taskID, Name: name, Store: artifacts.StoreVolume, Key: name,
			Kind: kind, Size: size,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	notesID := put("run-notes.txt", "", []byte("hello notes"), -1)
	secondID := put("later.txt", "", []byte("second"), -1)
	htmlID := put("page.html", "", []byte("<html></html>"), -1)
	svgID := put("icon.svg", "", []byte("<svg></svg>"), -1)
	imageID := put("photo.png", "", []byte("png"), -1)
	binaryID := put("data.bin", "", []byte{0xff, 0xfe}, -1)
	diffBody := []byte(`{"files":[{"path":"a.txt","before":"old","after":"new"}]}`)
	diffID := put("changes.diff", "", diffBody, -1)
	bigID := put("big.txt", "text", []byte("tiny"), artifacts.MaxViewBytes+1)

	listed := call(ownerToken, "/api/tasks/"+taskID+"/artifacts", false)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status %d body %s", listed.Code, listed.Body.String())
	}
	dec := json.NewDecoder(strings.NewReader(listed.Body.String()))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		t.Fatal(err)
	}
	gotIDs := make([]string, len(rows))
	for i, row := range rows {
		id, _ := row["id"].(string)
		gotIDs[i] = id
	}
	wantIDs := []string{notesID, secondID, htmlID, svgID, imageID, binaryID, diffID, bigID}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("order = %#v, want %#v", gotIDs, wantIDs)
	}
	size, ok := rows[0]["size"].(json.Number)
	if !ok || size.String() != "11" {
		t.Fatalf("size = %#v", rows[0]["size"])
	}
	created, _ := rows[0]["created_at"].(string)
	when, err := time.Parse(time.RFC3339, created)
	if err != nil || !strings.HasSuffix(created, "Z") || when.Location() != time.UTC {
		t.Fatalf("created_at = %q err %v", created, err)
	}
	if rows[0]["kind"] != "text" || rows[0]["mime"] != "text/plain" {
		t.Fatalf("notes meta = %#v", rows[0])
	}

	for _, id := range []string{htmlID, svgID} {
		for _, path := range []string{"/api/artifacts/" + id, "/api/artifacts/" + id + "/download"} {
			rec := call(ownerToken, path, false)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status %d", path, rec.Code)
			}
			if rec.Header().Get(artifacts.HeaderType) != artifacts.TypeBytes {
				t.Fatalf("%s type %q", path, rec.Header().Get(artifacts.HeaderType))
			}
			if !strings.Contains(rec.Header().Get(artifacts.HeaderDisposition), "attachment") {
				t.Fatalf("%s disposition %q", path, rec.Header().Get(artifacts.HeaderDisposition))
			}
			if rec.Header().Get(artifacts.HeaderSniff) != artifacts.SniffValue || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s headers %v", path, rec.Header())
			}
		}
	}

	text := call(ownerToken, "/api/artifacts/"+notesID+"/text", false)
	if text.Code != http.StatusOK || text.Body.String() != "hello notes" {
		t.Fatalf("text status %d body %q", text.Code, text.Body.String())
	}
	if text.Header().Get(artifacts.HeaderType) != "text/plain; charset=utf-8" {
		t.Fatalf("text type %q", text.Header().Get(artifacts.HeaderType))
	}
	if text.Header().Get(artifacts.HeaderSniff) != artifacts.SniffValue || text.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("text headers %v", text.Header())
	}
	svgText := call(ownerToken, "/api/artifacts/"+svgID+"/text", false)
	if svgText.Code != http.StatusOK || svgText.Body.String() != "<svg></svg>" || svgText.Header().Get(artifacts.HeaderType) != "text/plain; charset=utf-8" {
		t.Fatalf("svg text status %d type %q body %q", svgText.Code, svgText.Header().Get(artifacts.HeaderType), svgText.Body.String())
	}
	if got := call(ownerToken, "/api/artifacts/"+bigID+"/text", false); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large text = %d body %s", got.Code, got.Body.String())
	}
	for _, id := range []string{imageID, binaryID} {
		got := call(ownerToken, "/api/artifacts/"+id+"/text", false)
		if got.Code != http.StatusUnsupportedMediaType || !strings.Contains(got.Body.String(), "artifact is not text") {
			t.Fatalf("text %s = %d body %s", id, got.Code, got.Body.String())
		}
	}

	diff := call(ownerToken, "/api/artifacts/"+diffID+"/diff", false)
	if diff.Code != http.StatusOK {
		t.Fatalf("diff status %d body %s", diff.Code, diff.Body.String())
	}
	if diff.Header().Get(artifacts.HeaderType) != "application/json" || diff.Header().Get(artifacts.HeaderSniff) != artifacts.SniffValue {
		t.Fatalf("diff headers %v", diff.Header())
	}
	var parsed artifacts.Diff
	if err := json.Unmarshal(diff.Body.Bytes(), &parsed); err != nil || len(parsed.Files) != 1 || parsed.Files[0].Path != "a.txt" {
		t.Fatalf("diff body %s err %v", diff.Body.String(), err)
	}
	if got := call(ownerToken, "/api/artifacts/"+notesID+"/diff", false); got.Code != http.StatusUnsupportedMediaType || !strings.Contains(got.Body.String(), "artifact is not a diff") {
		t.Fatalf("notes diff = %d body %s", got.Code, got.Body.String())
	}

	otherList := call(memberToken, "/api/tasks/"+taskID+"/artifacts", false)
	if otherList.Code != http.StatusOK || strings.TrimSpace(otherList.Body.String()) != "[]" {
		t.Fatalf("unshared list = %d %s", otherList.Code, otherList.Body.String())
	}
	for _, path := range []string{
		"/api/artifacts/" + notesID + "/download",
		"/api/artifacts/" + notesID + "/text",
		"/api/artifacts/" + notesID + "/diff",
	} {
		if got := call(memberToken, path, false); got.Code != http.StatusNotFound {
			t.Fatalf("unshared %s = %d", path, got.Code)
		}
	}
	for _, path := range []string{
		"/api/tasks/" + taskID + "/artifacts",
		"/api/artifacts/" + notesID + "/download",
		"/api/artifacts/" + notesID + "/text",
		"/api/artifacts/" + notesID + "/diff",
	} {
		if got := call(lockedToken, path, false); got.Code != http.StatusForbidden {
			t.Fatalf("no view %s = %d body %s", path, got.Code, got.Body.String())
		}
	}
	if got := call(ownerToken, "/api/tasks/"+taskID+"/artifacts", true); got.Code != http.StatusForbidden {
		t.Fatalf("bearer = %d", got.Code)
	}
}

func startSession(t *testing.T, ctx context.Context, sessions interface {
	Start(context.Context, identity.UserID, string, string) (string, identity.Session, error)
}, userID string) string {
	t.Helper()
	uid, err := identity.ParseUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
