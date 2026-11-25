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

	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// reach opens a task and walks it to status with the real transitions.
func reach(t *testing.T, store postgres.Store, goal string, status domain.Status) app.Task {
	t.Helper()
	ctx := context.Background()
	task, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: goal})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[domain.Status][]domain.Input{
		domain.Preparing:     nil,
		domain.Planning:      {{Kind: domain.Plan}},
		domain.Working:       {{Kind: domain.Advance}},
		domain.Verifying:     {{Kind: domain.Advance}, {Kind: domain.Advance, Turn: domain.Sentinel}},
		domain.NeedsApproval: {{Kind: domain.Advance}, {Kind: domain.Pause}},
		domain.Done:          {{Kind: domain.Advance}, {Kind: domain.Advance, Turn: domain.Sentinel}, {Kind: domain.Pass}},
		domain.Failed:        {{Kind: domain.Plan}, {Kind: domain.Fail}},
	}
	for _, input := range steps[status] {
		if input.Kind == domain.Pass {
			if _, err := app.RecordChecks(ctx, store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
				t.Fatal(err)
			}
		}
		task, err = app.ApplyTransition(ctx, store, ownerAccess, task.ID, input)
		if err != nil {
			t.Fatalf("%s: %q: %v", status, input.Kind, err)
		}
	}
	if task.Status != status {
		t.Fatalf("reached %s, want %s", task.Status, status)
	}
	return task
}

func TestViewCollapsesEachStatusAndKeepsThePhase(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	tests := []struct {
		state  domain.Status
		status domain.Shown
		phase  domain.Status
	}{
		{domain.Preparing, domain.ShownWorking, domain.Preparing},
		{domain.Planning, domain.ShownWorking, domain.Planning},
		{domain.Working, domain.ShownWorking, domain.Working},
		{domain.Verifying, domain.ShownWorking, domain.Verifying},
		{domain.NeedsApproval, domain.ShownNeedsApproval, domain.Working},
		{domain.Done, domain.ShownDone, domain.Verifying},
		{domain.Failed, domain.ShownFailed, domain.Planning},
	}
	ids := map[domain.Status]string{}
	for _, test := range tests {
		ids[test.state] = reach(t, store, "Goal "+string(test.state), test.state).ID
	}

	listed, err := app.ListTasks(ctx, store, ownerAccess)
	if err != nil || len(listed) != len(tests) {
		t.Fatalf("list = %d %v", len(listed), err)
	}
	byID := map[string]app.TaskSummary{}
	for _, task := range listed {
		byID[task.ID] = task
	}
	for _, test := range tests {
		view, err := app.ViewTask(ctx, store, ownerAccess, ids[test.state])
		if err != nil {
			t.Fatalf("%s: %v", test.state, err)
		}
		for name, got := range map[string]app.TaskSummary{"view": view.TaskSummary, "list": byID[ids[test.state]]} {
			if got.State != test.state || got.Status != test.status || got.Phase != test.phase {
				t.Errorf("%s of %s: state %q status %q phase %q; want %q %q %q",
					name, test.state, got.State, got.Status, got.Phase, test.state, test.status, test.phase)
			}
		}
	}
}

func TestListShapeAndOrder(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	first := openRoot(t, store, "First")
	second := openRoot(t, store, "Second")
	if err := app.Spend(ctx, store, ownerAccess, first.ID, 120); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, first.ID, domain.Input{Kind: domain.Plan}); err != nil {
		t.Fatal(err)
	}

	listed, err := app.ListTasks(ctx, store, ownerAccess)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list = %+v %v", listed, err)
	}
	// Planning touched First last, so it comes first.
	if listed[0].ID != first.ID || listed[1].ID != second.ID {
		t.Fatalf("order = %s, %s", listed[0].Title, listed[1].Title)
	}
	got := listed[0]
	if got.Title != "First" || got.SpentMicroUSD != 120 || got.BudgetMicroUSD != 2_000_000 || got.Status != domain.ShownWorking || got.Phase != domain.Planning {
		t.Fatalf("first = %+v", got)
	}
	if got.UpdatedAt.IsZero() || got.UpdatedAt.Before(listed[1].UpdatedAt) {
		t.Fatalf("updated at = %v then %v", got.UpdatedAt, listed[1].UpdatedAt)
	}
}

func TestViewMergesTaskAndChatEventsInTimeOrder(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)
	step := func(input domain.Input) {
		t.Helper()
		if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	step(domain.Input{Kind: domain.Chat, Body: " Start with March "})
	// The turn text is stored as a chat line in the same transaction as the
	// advance event, so both carry one timestamp.
	step(domain.Input{Kind: domain.Advance, Turn: "March is filed\n" + domain.Sentinel})
	step(domain.Input{Kind: domain.Chat, Body: "Looks right"})

	view, err := app.ViewTask(ctx, store, ownerAccess, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	type line struct {
		phase   domain.Status
		kind    string
		message string
	}
	want := []line{
		{domain.Preparing, "open", "Task opened"},
		{domain.Working, "advance", "Moved on to Working"},
		{domain.Working, "message", "Start with March"},
		{domain.Working, "message", "March is filed"},
		{domain.Verifying, "advance", "Moved on to Verifying"},
		{domain.Verifying, "message", "Looks right"},
	}
	var got []line
	for i, event := range view.Events {
		got = append(got, line{event.Phase, event.Type, event.Message})
		if event.ID == "" || event.Time.IsZero() || (i > 0 && event.Time.Before(view.Events[i-1].Time)) {
			t.Fatalf("event %d = %+v", i, event)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v\nwant %+v", got, want)
	}
	if view.Status != domain.ShownWorking || view.Phase != domain.Verifying {
		t.Fatalf("status %q phase %q", view.Status, view.Phase)
	}
}

func TestViewListsOnlyDirectChildrenAsSubtasks(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	one, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Read the folder", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	two, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cancel(ctx, store, ownerAccess, two.ID); err != nil {
		t.Fatal(err)
	}

	view, err := app.ViewTask(ctx, store, ownerAccess, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []app.SubtaskView{
		{ID: one.ID, Title: "Read the folder", Status: domain.ShownWorking},
		{ID: two.ID, Title: "File them", Status: domain.ShownFailed},
	}
	if !reflect.DeepEqual(view.Subtasks, want) {
		t.Fatalf("subtasks = %+v, want %+v", view.Subtasks, want)
	}
	child, err := app.ViewTask(ctx, store, ownerAccess, one.ID)
	if err != nil || child.Subtasks == nil || len(child.Subtasks) != 0 {
		t.Fatalf("a child's subtasks = %#v %v", child.Subtasks, err)
	}
}

func TestViewListsOnlyTheSubtasksThePersonCanSee(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	shown, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Read the folder", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Private errand", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		access app.Access
		setup  string
	}{
		{
			name:   "parent and one child visible to everyone",
			access: otherAccess,
			setup:  `UPDATE tasks.tasks SET visibility = 'everyone' WHERE id IN ($1::uuid, $2::uuid)`,
		},
		{
			name: "parent and one child shared with the person",
			access: app.Access{
				UserID: otherUser, Allow: otherAccess.Allow,
				SharedView: "{" + parent.ID + "," + shown.ID + "}",
			},
			setup: `SELECT $1::uuid, $2::uuid`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'private' WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`,
				parent.ID, shown.ID, hidden.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(ctx, test.setup, parent.ID, shown.ID); err != nil {
				t.Fatal(err)
			}

			view, err := app.ViewTask(ctx, store, test.access, parent.ID)
			if err != nil {
				t.Fatalf("view = %v", err)
			}
			want := []app.SubtaskView{{ID: shown.ID, Title: "Read the folder", Status: domain.ShownWorking}}
			if !reflect.DeepEqual(view.Subtasks, want) {
				t.Fatalf("subtasks = %+v, want %+v", view.Subtasks, want)
			}
			if _, err := app.ViewTask(ctx, store, test.access, hidden.ID); !errors.Is(err, app.ErrGone) {
				t.Fatalf("hidden child view = %v", err)
			}

			listed, err := app.ListTasks(ctx, store, test.access)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, task := range listed {
				ids = append(ids, task.ID)
			}
			sort.Strings(ids)
			wantIDs := []string{parent.ID, shown.ID}
			sort.Strings(wantIDs)
			if !reflect.DeepEqual(ids, wantIDs) {
				t.Fatalf("list = %v, want %v", ids, wantIDs)
			}
		})
	}
}

func TestViewRendersResultAndDetail(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	view, err := app.ViewTask(ctx, store, ownerAccess, child.ID)
	if err != nil || view.Result != "" || view.Detail != "" {
		t.Fatalf("before = %q %q %v", view.Result, view.Detail, err)
	}

	// The database only holds the fixed result object, which renders as compact
	// JSON in jsonb key order. A JSON string result is covered by the rule test.
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, child.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, child.ID, "All filed", "two pages"); err != nil {
		t.Fatal(err)
	}
	view, err = app.ViewTask(ctx, store, ownerAccess, child.ID)
	if err != nil || view.Result != `{"detail":"two pages","summary":"All filed"}` {
		t.Fatalf("object result = %q %v", view.Result, err)
	}

	if _, err := app.Cancel(ctx, store, ownerAccess, parent.ID); err != nil {
		t.Fatal(err)
	}
	view, err = app.ViewTask(ctx, store, ownerAccess, child.ID)
	if err != nil || view.Detail != domain.ParentCanceled || view.Status != domain.ShownFailed {
		t.Fatalf("canceled child = %q %q %v", view.Detail, view.Status, err)
	}
}

func TestAnotherPersonSeesNothing(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	task := reach(t, store, "Sort the mail", domain.Working)

	listed, err := app.ListTasks(ctx, store, otherAccess)
	if err != nil || listed == nil || len(listed) != 0 {
		t.Fatalf("other list = %#v %v", listed, err)
	}
	if _, err := app.ViewTask(ctx, store, otherAccess, task.ID); !errors.Is(err, app.ErrGone) {
		t.Fatalf("other view = %v", err)
	}
}

func TestHTTPServesTheReadModel(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	serve := func(access app.Access) http.Handler {
		mux := http.NewServeMux()
		tasks.Handler{
			Book: store, List: store, View: store,
			Access: func(*http.Request) (app.Access, bool) { return access, true },
		}.Mount(mux, nil)
		return mux
	}
	owned, others := serve(ownerAccess), serve(otherAccess)
	do := func(h http.Handler, method, path, body string) (int, []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code, rec.Body.Bytes()
	}
	keys := func(raw []byte) []string {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	hasKeys := func(raw []byte, want ...string) {
		t.Helper()
		got := keys(raw)
		if len(got) != len(want) {
			t.Fatalf("keys = %v, want %v", got, want)
		}
		for _, k := range want {
			var present bool
			for _, g := range got {
				present = present || g == k
			}
			if !present {
				t.Fatalf("keys = %v, want %v", got, want)
			}
		}
	}

	code, body := do(owned, http.MethodPost, "/api/tasks", `{"goal":"File the receipts"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	hasKeys(body, "id", "status", "goal")
	var created struct{ ID string }
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/" + created.ID

	code, body = do(owned, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("open get = %d %s", code, body)
	}
	var opened struct {
		Spent  int64 `json:"spent_micro_usd"`
		Budget int64 `json:"budget_micro_usd"`
	}
	if err := json.Unmarshal(body, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.Spent != 0 || opened.Budget != 2_000_000 {
		t.Fatalf("opened money = %+v", opened)
	}

	code, body = do(owned, http.MethodPost, path, `{"kind":"chat","body":" Start with March "}`)
	if code != http.StatusCreated {
		t.Fatalf("chat = %d %s", code, body)
	}
	hasKeys(body, "id", "time", "phase", "type", "message")
	var posted struct{ ID, Time, Phase, Type, Message string }
	if err := json.Unmarshal(body, &posted); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, posted.Time); err != nil {
		t.Fatalf("time = %q", posted.Time)
	}
	if posted.ID == "" || posted.Phase != "Preparing" || posted.Type != "message" || posted.Message != "Start with March" {
		t.Fatalf("posted = %+v", posted)
	}

	code, body = do(owned, http.MethodPost, path, `{"kind":"advance"}`)
	if code != http.StatusOK {
		t.Fatalf("advance = %d %s", code, body)
	}
	hasKeys(body, "id", "title", "status", "phase", "spent_micro_usd", "budget_micro_usd", "updated_at", "result", "detail", "subtasks", "events")
	var detail struct {
		ID, Title, Status, Phase, Result, Detail string
		Subtasks                                 []json.RawMessage
		Events                                   []struct{ ID, Phase, Type, Message string }
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != created.ID || detail.Title != "File the receipts" || detail.Status != "Working" || detail.Phase != "Working" ||
		detail.Result != "" || detail.Detail != "" || detail.Subtasks == nil || len(detail.Subtasks) != 0 || len(detail.Events) != 3 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Events[1].ID != posted.ID || detail.Events[2].Type != "advance" || detail.Events[2].Message != "Moved on to Working" {
		t.Fatalf("events = %+v", detail.Events)
	}

	code, got := do(owned, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("get = %d %s", code, got)
	}
	var again map[string]any
	var first map[string]any
	if err := json.Unmarshal(got, &again); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("get differs from the step answer:\n%s\n%s", body, got)
	}

	code, body = do(owned, http.MethodGet, "/api/tasks", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil || len(items) != 1 {
		t.Fatalf("list = %s %v", body, err)
	}
	hasKeys(items[0], "id", "title", "status", "phase", "spent_micro_usd", "budget_micro_usd", "updated_at")

	code, body = do(others, http.MethodGet, path, "")
	if code != http.StatusNotFound || strings.TrimSpace(string(body)) != `{"error":"no such task"}` {
		t.Fatalf("other get = %d %s", code, body)
	}
	code, body = do(others, http.MethodGet, "/api/tasks", "")
	if code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Fatalf("other list = %d %s", code, body)
	}
	code, body = do(others, http.MethodPost, path, `{"kind":"chat","body":"hello"}`)
	if code != http.StatusNotFound {
		t.Fatalf("other chat = %d %s", code, body)
	}

	code, body = do(owned, http.MethodPost, path, `{"kind":"fund","amount":2500000}`)
	if code != http.StatusOK {
		t.Fatalf("fund = %d %s", code, body)
	}
	if strings.Contains(string(body), "spent_cents") || strings.Contains(string(body), "limit_cents") {
		t.Fatalf("fund body uses old money keys: %s", body)
	}
	var funded struct {
		Spent  int64 `json:"spent_micro_usd"`
		Budget int64 `json:"budget_micro_usd"`
	}
	if err := json.Unmarshal(body, &funded); err != nil {
		t.Fatal(err)
	}
	if funded.Spent != 0 || funded.Budget != 2_500_000 {
		t.Fatalf("funded money = %+v", funded)
	}
}
