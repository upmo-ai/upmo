//go:build integration

package audit_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// tasksRig migrates audit, identity and tasks on a fresh database and returns
// the owner pool and the request pool that runs as upmo_app.
func tasksRig(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	return owner, pgtest.App(t, owner)
}

var taskAccess = app.Access{
	UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
	Allow:  "task:create,task:view,task:edit",
}

// openWorkingTask opens a task and walks it to Working, one audited step at a
// time.
func openWorkingTask(t *testing.T, store tasks.Store) app.Task {
	t.Helper()
	ctx := context.Background()
	task, err := store.Transition(ctx, taskAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil || task.Status != domain.Preparing {
		t.Fatalf("open = %+v %v", task, err)
	}
	working, err := store.Transition(ctx, taskAccess, task.ID, domain.Input{Kind: domain.Advance})
	if err != nil || working.Status != domain.Working {
		t.Fatalf("advance = %+v %v", working, err)
	}
	return working
}

func TestTaskTransitionsSealOneActionEach(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)
	ctx := context.Background()

	task := openWorkingTask(t, store)

	// Working hands over with the turn sentinel and enters Checking.
	checked, err := store.Transition(ctx, taskAccess, task.ID, domain.Input{Kind: domain.Advance, Turn: domain.Sentinel})
	if err != nil || checked.Status != domain.Checking {
		t.Fatalf("advance = %+v %v", checked, err)
	}
	// A chat line keeps the status and records the line, not the text.
	noted, err := store.Transition(ctx, taskAccess, task.ID, domain.Input{Kind: domain.Chat, Body: "the confidential figure"})
	if err != nil {
		t.Fatal(err)
	}
	if noted.ChatID == "" {
		t.Fatal("the chat step stored no line")
	}
	// The pass needs core evidence first, then ends the task.
	if passed, err := store.Record(ctx, taskAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}); err != nil || !passed {
		t.Fatalf("checks = %t %v", passed, err)
	}
	done, err := store.Transition(ctx, taskAccess, task.ID, domain.Input{Kind: domain.Pass})
	if err != nil || done.Status != domain.Done {
		t.Fatalf("pass = %+v %v", done, err)
	}

	rows := sealed()
	if len(rows) != 5 {
		t.Fatalf("%d events, want open, two advances, chat and pass", len(rows))
	}
	for _, row := range rows {
		// The writers ran with no request scope, so the system fallback acts.
		if row.ActorType != "system" || row.CredentialType != "none" {
			t.Fatalf("%s: actor = %s %s, want the system fallback of a writer with no request", row.Action, row.ActorType, row.CredentialType)
		}
		if row.Outcome != audit.Success || row.Reason != "" {
			t.Fatalf("%s: outcome %s reason %q", row.Action, row.Outcome, row.Reason)
		}
	}
	if rows[0].Action != audit.TaskCreate || rows[0].TargetID != task.ID {
		t.Fatalf("first event = %s target %s, want the created task %s", rows[0].Action, rows[0].TargetID, task.ID)
	}
	if hasAnyValue(rows[0]) {
		t.Fatalf("create changes = %s, want field names only", changesJSON(t, rows[0]))
	}
	if rows[1].Action != audit.TaskUpdate {
		t.Fatalf("second event = %s, want task.update", rows[1].Action)
	}
	if from, to := statusEdge(t, rows[1]); from != string(domain.Preparing) || to != string(domain.Working) {
		t.Fatalf("update status = %s -> %s", from, to)
	}
	if rows[2].Action != audit.TaskUpdate {
		t.Fatalf("third event = %s, want the second task.update", rows[2].Action)
	}
	if from, to := statusEdge(t, rows[2]); from != string(domain.Working) || to != string(domain.Checking) {
		t.Fatalf("update status = %s -> %s", from, to)
	}
	if rows[3].Action != audit.TaskChat || rows[3].TargetType != "line" || rows[3].TargetID != noted.ChatID {
		t.Fatalf("chat event = %s %s %s, want the stored line %s", rows[3].Action, rows[3].TargetType, rows[3].TargetID, noted.ChatID)
	}
	if containsSecret(rows[3], "the confidential figure") {
		t.Fatalf("chat changes hold the line text: %s", changesJSON(t, rows[3]))
	}
	if rows[4].Action != audit.TaskStatusChange {
		t.Fatalf("fifth event = %s, want task.status_change", rows[4].Action)
	}
	if from, to := statusEdge(t, rows[4]); from != string(domain.Checking) || to != string(domain.Done) {
		t.Fatalf("status change = %s -> %s", from, to)
	}
}

func TestTaskFailSealsTheStatusChange(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)
	ctx := context.Background()

	task := openWorkingTask(t, store)
	sealed()

	failed, err := store.Transition(ctx, taskAccess, task.ID, domain.Input{Kind: domain.Fail})
	if err != nil || failed.Status != domain.Failed {
		t.Fatalf("fail = %+v %v", failed, err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.TaskStatusChange || rows[0].TargetID != task.ID {
		t.Fatalf("events = %v, want one task.status_change for %s", actions(rows), task.ID)
	}
	if from, to := statusEdge(t, rows[0]); from != string(domain.Working) || to != string(domain.Failed) {
		t.Fatalf("status = %s -> %s, want working to failed", from, to)
	}
}

func TestTaskRunSealsTheSystemAdmission(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)
	ctx := context.Background()

	task := openWorkingTask(t, store)
	sealed()
	run, err := store.Admit(ctx, taskAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 1 {
		t.Fatalf("%d events, want the one admission", len(rows))
	}
	row := rows[0]
	if row.Action != audit.TaskRun || row.TargetType != "run" || row.TargetID != run.ID {
		t.Fatalf("event = %s %s %s, want the admitted run %s", row.Action, row.TargetType, row.TargetID, run.ID)
	}
	// A run admission comes from the queue, so its writer seeds the system
	// actor with the run credential when no request scope exists.
	if row.ActorType != "system" || row.CredentialType != "run" {
		t.Fatalf("actor = %s %s, want system run", row.ActorType, row.CredentialType)
	}
	state := changeOf(t, row, "state")
	if state.To == "" || state.To == state.From {
		t.Fatalf("state = %v -> %v, want the admitted edge", state.From, state.To)
	}
}

func TestTaskShareSealsGrantsAndVisibility(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)
	ctx := context.Background()

	task := openWorkingTask(t, store)
	sealed()

	before := []domain.ShareGrant{{SubjectType: domain.SharePerson, SubjectID: "018f3b2c-7a10-7c4a-8e11-222222222222", Role: domain.ShareViewer}}
	after := []domain.ShareGrant{{SubjectType: domain.SharePerson, SubjectID: "018f3b2c-7a10-7c4a-8e11-222222222222", Role: domain.ShareEditor}}
	if err := store.SaveVisibility(ctx, taskAccess, task.ID, domain.VisibilityTeam, "018f3b2c-7a10-7c4a-8e11-333333333333", before, after); err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.TaskShare {
		t.Fatalf("events = %v, want one task.share", actions(rows))
	}
	row := rows[0]
	if row.TargetID != task.ID {
		t.Fatalf("target = %s, want the shared task %s", row.TargetID, task.ID)
	}
	grants := changeOf(t, row, "grants")
	beforeList, ok := grants.From.([]any)
	if !ok || len(beforeList) != 1 {
		t.Fatalf("grants from = %v, want the one prior grant", grants.From)
	}
	afterList, ok := grants.To.([]any)
	if !ok || len(afterList) != 1 {
		t.Fatalf("grants to = %v, want the one new grant", grants.To)
	}
	entry, _ := afterList[0].(map[string]any)
	if entry["subject_type"] != domain.SharePerson || entry["level"] != domain.ShareEditor {
		t.Fatalf("grant = %v, want subject type and level", entry)
	}
	if _, hasID := entry["subject_id"]; !hasID {
		t.Fatalf("grant = %v, want the subject id", entry)
	}
	visibility := changeOf(t, row, "visibility")
	if visibility.From != domain.VisibilityPrivate || visibility.To != domain.VisibilityTeam {
		t.Fatalf("visibility = %v -> %v", visibility.From, visibility.To)
	}
}

func TestAutomationFireSealsTheSystemCreate(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)
	ctx := context.Background()

	templateID, err := app.SaveTemplate(ctx, store, taskAccess, "", domain.Template{
		Goal:    "File the {{kind}}",
		AgentID: "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Budget:  40,
	})
	if err != nil {
		t.Fatal(err)
	}
	automationID, err := app.SaveAutomation(ctx, store, taskAccess, templateID, domain.KindCron, "0 9 * * 1")
	if err != nil {
		t.Fatal(err)
	}
	sealed()

	opened, reused, err := app.Fire(ctx, store, taskAccess, automationID, "monday", map[string]string{"kind": "receipts"}, "")
	if err != nil || reused || opened == "" {
		t.Fatalf("fire = %s reused %t %v", opened, reused, err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.TaskCreate || rows[0].TargetID != opened {
		t.Fatalf("events = %v, want the fired task's task.create", actions(rows))
	}
	// The automation fired on its own schedule, so the system acted, with the
	// run credential of the fire.
	if rows[0].ActorType != "system" || rows[0].CredentialType != "run" {
		t.Fatalf("actor = %s %s, want system run", rows[0].ActorType, rows[0].CredentialType)
	}

	// The same dedup key opens nothing new, so it seals nothing either.
	again, reused, err := app.Fire(ctx, store, taskAccess, automationID, "monday", map[string]string{"kind": "other"}, "")
	if err != nil || !reused || again != opened {
		t.Fatalf("refire = %s reused %t %v", again, reused, err)
	}
	if rows := sealed(); len(rows) != 0 {
		t.Fatalf("the reused fire sealed %v", actions(rows))
	}
}

func TestTaskRoutesSealTheirOwnEvents(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)

	mux := http.NewServeMux()
	tasks.Handler{
		Book: store,
		List: store,
		View: store,
		Access: func(r *http.Request) (app.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: taskAccess.UserID, CredentialType: "session"})
			return taskAccess, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"goal":"File the receipts"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("open = %d %s", rec.Code, rec.Body.String())
	}
	var opened struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opened); err != nil || opened.ID == "" {
		t.Fatalf("open body = %s err %v", rec.Body.String(), err)
	}

	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tasks/"+opened.ID, strings.NewReader(`{"kind":"advance"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("advance = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 2 || rows[0].Action != audit.TaskCreate || rows[1].Action != audit.TaskUpdate {
		t.Fatalf("events = %v, want the create and the advance's task.update", actions(rows))
	}
	for _, row := range rows {
		// The store sealed the event in its own transaction, so the middleware
		// had nothing to fall back on: the reason is empty, not unrecorded.
		if row.Reason != "" {
			t.Fatalf("%s: reason = %q, want the store's own event, not the unrecorded fallback", row.Action, row.Reason)
		}
		if row.ActorType != "user" || row.ActorID != taskAccess.UserID || row.CredentialType != "session" {
			t.Fatalf("%s: actor = %s %s %s", row.Action, row.ActorType, row.ActorID, row.CredentialType)
		}
	}
	if rows[0].TargetID != opened.ID || rows[1].TargetID != opened.ID {
		t.Fatalf("targets = %s %s, want the one task %s", rows[0].TargetID, rows[1].TargetID, opened.ID)
	}
}

// shareGrants joins identity's member service to the tasks share port, the
// way brain wires it, so the WriteShare test replaces real grants.
type shareGrants struct {
	members identity.MemberService
}

func (g shareGrants) TaskGrants(ctx context.Context, taskID string) ([]app.ShareMember, error) {
	rows, err := g.members.TaskGrants(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]app.ShareMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, app.ShareMember{
			SubjectType: row.SubjectType,
			SubjectID:   row.SubjectID,
			Name:        row.Name,
			Role:        row.Level,
		})
	}
	return out, nil
}

func (g shareGrants) ReplaceTaskGrants(ctx context.Context, taskID string, grants []domain.ShareGrant, grantedBy string) error {
	by, err := identity.ParseUserID(grantedBy)
	if err != nil {
		return err
	}
	writes := make([]identity.GrantWrite, 0, len(grants))
	for _, grant := range grants {
		writes = append(writes, identity.GrantWrite{
			SubjectType: grant.SubjectType,
			SubjectID:   grant.SubjectID,
			Level:       grant.Role,
		})
	}
	return g.members.ReplaceTaskGrants(ctx, taskID, writes, by)
}

func (g shareGrants) replaceTaskGrantsTx(ctx context.Context, tx pgx.Tx, taskID string, grants []domain.ShareGrant, grantedBy string) error {
	by, err := identity.ParseUserID(grantedBy)
	if err != nil {
		return err
	}
	writes := make([]identity.GrantWrite, 0, len(grants))
	for _, grant := range grants {
		writes = append(writes, identity.GrantWrite{
			SubjectType: grant.SubjectType,
			SubjectID:   grant.SubjectID,
			Level:       grant.Role,
		})
	}
	return g.members.ReplaceTaskGrantsTx(ctx, tx, taskID, writes, by)
}

func taskShareStore(appPool *pgxpool.Pool, grants shareGrants) tasks.Store {
	store := tasks.NewStore(appPool, nil)
	store.ReplaceTaskGrants = grants.replaceTaskGrantsTx
	return store
}

func (g shareGrants) Names(ctx context.Context, persons, teams []string) (map[string]string, map[string]string, error) {
	return g.members.NamesOf(ctx, persons, teams)
}

func (g shareGrants) TeamsOf(ctx context.Context, person string) ([]string, error) {
	id, err := identity.ParseUserID(person)
	if err != nil {
		return nil, err
	}
	return g.members.TeamsOf(ctx, id)
}

func TestTaskShareWriteSealsTheGrantsBeforeAndAfter(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	ctx := context.Background()

	ada := insertPerson(t, owner, "ada@example.com")
	sam := insertPerson(t, owner, "sam@example.com")
	grants := shareGrants{members: identity.Members(appPool, "", nil, nil)}
	store := taskShareStore(appPool, grants)
	ownerAccess := app.Access{UserID: ada, Allow: "task:create,task:view,task:edit"}

	task, err := store.Transition(ctx, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Close the books"})
	if err != nil || task.Status != domain.Preparing {
		t.Fatalf("open = %+v %v", task, err)
	}
	sealed()

	ownerEntry := domain.ShareGrant{SubjectType: domain.SharePerson, SubjectID: ada, Role: domain.ShareOwner}
	// The first write grants Sam a viewer: the share stood empty before it.
	if _, err := app.WriteShare(ctx, store, grants, ownerAccess, task.ID, domain.ShareUpdate{
		Visibility: domain.VisibilityPrivate,
		Grants:     []domain.ShareGrant{ownerEntry, {SubjectType: domain.SharePerson, SubjectID: sam, Role: domain.ShareViewer}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.TaskShare {
		t.Fatalf("events = %v, want one task.share", actions(rows))
	}
	if rows[0].TargetID != task.ID {
		t.Fatalf("target = %s, want the shared task %s", rows[0].TargetID, task.ID)
	}
	if edge := changeOf(t, rows[0], "grants"); len(edge.From.([]any)) != 0 || len(edge.To.([]any)) != 1 {
		t.Fatalf("grants = %v -> %v, want none before and the viewer after", edge.From, edge.To)
	}

	// The second write raises Sam to editor, so the event holds the viewer it
	// replaced and the editor it replaced it with.
	if _, err := app.WriteShare(ctx, store, grants, ownerAccess, task.ID, domain.ShareUpdate{
		Visibility: domain.VisibilityPrivate,
		Grants:     []domain.ShareGrant{ownerEntry, {SubjectType: domain.SharePerson, SubjectID: sam, Role: domain.ShareEditor}},
	}); err != nil {
		t.Fatal(err)
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.TaskShare {
		t.Fatalf("events = %v, want the second task.share", actions(rows))
	}
	edge := changeOf(t, rows[0], "grants")
	before, ok := edge.From.([]any)
	if !ok || len(before) != 1 {
		t.Fatalf("grants from = %v, want the viewer it replaced", edge.From)
	}
	entry, _ := before[0].(map[string]any)
	if entry["level"] != domain.ShareViewer || entry["subject_id"] != sam {
		t.Fatalf("grant before = %v, want Sam's viewer", entry)
	}
	after, ok := edge.To.([]any)
	if !ok || len(after) != 1 {
		t.Fatalf("grants to = %v, want the editor it stands at now", edge.To)
	}
	entry, _ = after[0].(map[string]any)
	if entry["level"] != domain.ShareEditor || entry["subject_id"] != sam {
		t.Fatalf("grant after = %v, want Sam's editor", entry)
	}
}

func TestTaskShareWriteRollsBackWhenAuditInsertFails(t *testing.T) {
	owner, appPool := tasksRig(t)
	ctx := context.Background()

	ada := insertPerson(t, owner, "ada@example.com")
	sam := insertPerson(t, owner, "sam@example.com")
	grants := shareGrants{members: identity.Members(appPool, "", nil, nil)}
	store := taskShareStore(appPool, grants)
	ownerAccess := app.Access{UserID: ada, Allow: "task:create,task:view,task:edit"}

	task, err := store.Transition(ctx, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Close the books"})
	if err != nil || task.Status != domain.Preparing {
		t.Fatalf("open = %+v %v", task, err)
	}
	ownerEntry := domain.ShareGrant{SubjectType: domain.SharePerson, SubjectID: ada, Role: domain.ShareOwner}
	if _, err := app.WriteShare(ctx, store, grants, ownerAccess, task.ID, domain.ShareUpdate{
		Visibility: domain.VisibilityPrivate,
		Grants:     []domain.ShareGrant{ownerEntry, {SubjectType: domain.SharePerson, SubjectID: sam, Role: domain.ShareViewer}},
	}); err != nil {
		t.Fatal(err)
	}
	var visibility string
	if err := owner.QueryRow(ctx, `SELECT visibility FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&visibility); err != nil {
		t.Fatal(err)
	}
	if visibility != domain.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private after the first write", visibility)
	}

	if _, err := owner.Exec(ctx, `REVOKE INSERT ON audit.pending FROM upmo_app`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `GRANT INSERT ON audit.pending TO upmo_app`); err != nil {
			t.Errorf("restore audit.pending insert: %v", err)
		}
	})

	if _, err := app.WriteShare(ctx, store, grants, ownerAccess, task.ID, domain.ShareUpdate{
		Visibility: domain.VisibilityEveryone,
		Grants:     []domain.ShareGrant{ownerEntry, {SubjectType: domain.SharePerson, SubjectID: sam, Role: domain.ShareEditor}},
	}); err == nil {
		t.Fatal("the share write succeeded without audit.insert")
	}
	if err := owner.QueryRow(ctx, `SELECT visibility FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&visibility); err != nil {
		t.Fatal(err)
	}
	if visibility != domain.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private unchanged", visibility)
	}
	rows, err := owner.Query(ctx, `
		SELECT share.level FROM identity.shares AS share
		WHERE share.object_type = 'task' AND share.object_id = $1::uuid
		  AND share.user_id = $2::uuid`, task.ID, sam)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("Sam's grant vanished")
	}
	var level string
	if err := rows.Scan(&level); err != nil {
		t.Fatal(err)
	}
	if level != domain.ShareViewer {
		t.Fatalf("Sam's level = %q, want viewer unchanged", level)
	}
}

func TestTaskShareWriteRollsBackGrantsWhenVisibilityFails(t *testing.T) {
	owner, appPool := tasksRig(t)
	ctx := context.Background()

	ada := insertPerson(t, owner, "ada@example.com")
	sam := insertPerson(t, owner, "sam@example.com")
	grants := shareGrants{members: identity.Members(appPool, "", nil, nil)}
	store := taskShareStore(appPool, grants)
	ownerAccess := app.Access{UserID: ada, Allow: "task:create,task:view,task:edit"}

	task, err := store.Transition(ctx, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Close the books"})
	if err != nil || task.Status != domain.Preparing {
		t.Fatalf("open = %+v %v", task, err)
	}
	ownerEntry := domain.ShareGrant{SubjectType: domain.SharePerson, SubjectID: ada, Role: domain.ShareOwner}
	if _, err := app.WriteShare(ctx, store, grants, ownerAccess, task.ID, domain.ShareUpdate{
		Visibility: domain.VisibilityPrivate,
		Grants:     []domain.ShareGrant{ownerEntry, {SubjectType: domain.SharePerson, SubjectID: sam, Role: domain.ShareViewer}},
	}); err != nil {
		t.Fatal(err)
	}

	// team visibility without a team id violates tasks_visibility_check after
	// the grants would have been replaced.
	if err := store.SaveShare(ctx, ownerAccess, task.ID, domain.VisibilityTeam, "", []domain.ShareGrant{
		ownerEntry, {SubjectType: domain.SharePerson, SubjectID: sam, Role: domain.ShareEditor},
	}, ada); err == nil {
		t.Fatal("team visibility without a team id should fail")
	}
	var visibility string
	if err := owner.QueryRow(ctx, `SELECT visibility FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&visibility); err != nil {
		t.Fatal(err)
	}
	if visibility != domain.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private unchanged", visibility)
	}
	var level string
	if err := owner.QueryRow(ctx, `
		SELECT share.level FROM identity.shares AS share
		WHERE share.object_type = 'task' AND share.object_id = $1::uuid
		  AND share.user_id = $2::uuid`, task.ID, sam).Scan(&level); err != nil {
		t.Fatal(err)
	}
	if level != domain.ShareViewer {
		t.Fatalf("Sam's level = %q, want viewer unchanged", level)
	}
}

func insertPerson(t *testing.T, owner *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	err := owner.QueryRow(context.Background(), `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, $2, (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`, email, email).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestApprovalDecisionsSealApproveAndDeny(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)
	ctx := context.Background()

	working := openWorkingTask(t, store)
	sealed()
	asked, err := store.Request(ctx, taskAccess, working.ID, domain.Ask{
		Tool:   "mail.send",
		Reason: "ask",
		Action: "Send the April invoice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows := sealed(); len(rows) != 0 {
		t.Fatalf("the ask sealed %v, want nothing: the ask is the run's own step", actions(rows))
	}

	decided, err := store.Decide(ctx, taskAccess, asked.ID, domain.Approve)
	if err != nil || decided.State != domain.ApprovalApproved {
		t.Fatalf("approve = %+v %v", decided, err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ApprovalApprove {
		t.Fatalf("events = %v, want one approval.approve", actions(rows))
	}
	state := changeOf(t, rows[0], "state")
	if state.From != string(domain.ApprovalPending) || state.To != string(domain.ApprovalApproved) {
		t.Fatalf("state = %v -> %v", state.From, state.To)
	}
	if containsSecret(rows[0], "Send the April invoice") {
		t.Fatalf("approve changes hold the ask text: %s", changesJSON(t, rows[0]))
	}

	asked, err = store.Request(ctx, taskAccess, working.ID, domain.Ask{Tool: "mail.send", Reason: "ask", Action: "Send it again"})
	if err != nil {
		t.Fatal(err)
	}
	sealed()
	denied, err := store.Decide(ctx, taskAccess, asked.ID, domain.Deny)
	if err != nil || denied.State != domain.ApprovalDenied {
		t.Fatalf("deny = %+v %v", denied, err)
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.ApprovalDeny {
		t.Fatalf("events = %v, want one approval.deny", actions(rows))
	}
	state = changeOf(t, rows[0], "state")
	if state.To != string(domain.ApprovalDenied) {
		t.Fatalf("state = %v -> %v, want the denial", state.From, state.To)
	}
}

func TestTaskEditorOpenSealsItsSensitiveEvent(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)

	task := openWorkingTask(t, store)
	sealed()

	editor := &stubEditorSandboxes{url: "http://127.0.0.1:9999"}
	mux := http.NewServeMux()
	tasks.Handler{
		View:         store,
		PublicURL:    "http://localhost:5173",
		Editor:       editor,
		EditorSigner: stubEditorSigner{},
		Audit:        appPool,
		Access: func(r *http.Request) (app.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: taskAccess.UserID, CredentialType: "session"})
			return taskAccess, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/editor", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("editor = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 {
		t.Fatalf("%d events, want the one editor open", len(rows))
	}
	row := rows[0]
	if row.Action != audit.TaskEditorOpen || row.TargetID != task.ID {
		t.Fatalf("event = %s target %s, want the editor open of %s", row.Action, row.TargetID, task.ID)
	}
	// The adapter recorded the event, so the middleware left no fallback.
	if row.Reason != "" {
		t.Fatalf("reason = %q, want the handler's own event", row.Reason)
	}
	if row.ActorType != "user" || row.ActorID != taskAccess.UserID || row.CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.CredentialType)
	}
	if hasAnyValue(row) {
		t.Fatalf("changes = %s, want no values", changesJSON(t, row))
	}
}

func TestTaskRouteRefusalRecordsDenied(t *testing.T) {
	owner, appPool := tasksRig(t)
	sealed := newSealer(t, owner)
	store := tasks.NewStore(appPool, nil)

	stranger := app.Access{UserID: "018f3b2c-7a10-7c4a-8e11-444444444444"}
	mux := http.NewServeMux()
	tasks.Handler{
		Book: store,
		List: store,
		View: store,
		Access: func(r *http.Request) (app.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: stranger.UserID, CredentialType: "session"})
			// Signed in, but no task permission: the handler refuses.
			return stranger, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"goal":"a goal nobody may set"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a person without task permission = %d %s, want 403", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.TaskCreate {
		t.Fatalf("events = %v, want one denied task.create", actions(rows))
	}
	if rows[0].Outcome != audit.Denied {
		t.Fatalf("outcome = %s, want denied", rows[0].Outcome)
	}
	if rows[0].ActorType != "user" || rows[0].ActorID != stranger.UserID {
		t.Fatalf("actor = %s %s, want the refused person", rows[0].ActorType, rows[0].ActorID)
	}
	if containsSecret(rows[0], "a goal nobody may set") {
		t.Fatalf("denied event holds the refused goal: %s", changesJSON(t, rows[0]))
	}
}

// stubEditorSandboxes and stubEditorSigner stand in for the sandbox sidecar
// and the token signer, which the editor open needs to succeed.
type stubEditorSandboxes struct{ url string }

func (s *stubEditorSandboxes) StartEditor(context.Context, string, string) (runtime.Endpoint, error) {
	return runtime.Endpoint{URL: s.url}, nil
}

type stubEditorSigner struct{}

func (stubEditorSigner) Sign(string, string, string, time.Time) (string, error) { return "signed", nil }

// serveWithAudit mounts the audit middleware around a mux the way brain does:
// the middleware outside the cross-origin guard, the guard around the mux.
func serveWithAudit(t *testing.T, mux *http.ServeMux, app *pgxpool.Pool) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return audit.Middleware(audit.Routes(), mux, app, "brain", nil, log)(httpserver.Protect(mux))
}
