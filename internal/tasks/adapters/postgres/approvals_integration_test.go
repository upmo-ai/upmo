//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const askAction = "Send the March invoice to Sam"

// approvalJSON is the wire shape of one approval.
type approvalJSON struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	Action string `json:"action"`
	State  string `json:"state"`
}

// request pauses a Working task through the use case and returns the approval.
func request(t *testing.T, store postgres.Store, taskID string) app.Approval {
	t.Helper()
	approval, err := app.RequestApproval(context.Background(), store, ownerAccess, taskID, "mail.send", "ask", askAction)
	if err != nil {
		t.Fatal(err)
	}
	return approval
}

// approvalServer mounts the routes. The X-As header picks who is signed in, and
// a request with no X-As header has no session.
func approvalServer(store postgres.Store, people map[string]app.Access) http.Handler {
	mux := http.NewServeMux()
	tasks.Handler{
		StepUp: func(*http.Request) bool { return true },
		Book:   store, List: store, View: store, Approvals: store,
		Access: func(r *http.Request) (app.Access, bool) {
			access, ok := people[r.Header.Get("X-As")]
			return access, ok
		},
	}.Mount(mux, nil)
	return mux
}

func send(h http.Handler, as, method, path string) (int, []byte) {
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	if as != "" {
		req.Header.Set("X-As", as)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return out
}

// lines are a task's history as "type: message".
func lines(t *testing.T, store postgres.Store, access app.Access, taskID string) []string {
	t.Helper()
	view, err := app.ViewTask(context.Background(), store, access, taskID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(view.Events))
	for _, event := range view.Events {
		out = append(out, event.Type+": "+event.Message)
	}
	return out
}

func equalLines(got, want []string) bool {
	return strings.Join(got, "\n") == strings.Join(want, "\n")
}

func approvalRows(t *testing.T, owner *pgxpool.Pool, taskID string) (total, pending int) {
	t.Helper()
	err := owner.QueryRow(context.Background(), `
		SELECT count(*), count(*) FILTER (WHERE state = 'pending')
		FROM tasks.approvals WHERE task_id = $1`, taskID).Scan(&total, &pending)
	if err != nil {
		t.Fatal(err)
	}
	return total, pending
}

func TestRequestApprovalPausesTheTaskAndStoresOnePendingRow(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)

	approval, err := app.RequestApproval(ctx, store, ownerAccess, task.ID, " mail.send ", "ask", "  "+askAction+"  ")
	if err != nil {
		t.Fatal(err)
	}
	if approval.ID == "" || approval.TaskID != task.ID || approval.Title != "File the receipts" ||
		approval.Action != askAction || approval.State != domain.ApprovalPending {
		t.Fatalf("approval = %+v", approval)
	}
	if got := taskStatus(t, owner, task.ID); got != string(domain.NeedsApproval) {
		t.Fatalf("task status = %s", got)
	}
	var user, tool, reason, state string
	var decidedNull bool
	if err := owner.QueryRow(ctx, `
		SELECT user_id::text, tool, reason, state, decided_at IS NULL
		FROM tasks.approvals WHERE id = $1`, approval.ID).Scan(&user, &tool, &reason, &state, &decidedNull); err != nil {
		t.Fatal(err)
	}
	if user != ownerUser || tool != "mail.send" || reason != "ask" || state != "pending" || !decidedNull {
		t.Fatalf("row = %s %s %s %s %t", user, tool, reason, state, decidedNull)
	}
	if total, pending := approvalRows(t, owner, task.ID); total != 1 || pending != 1 {
		t.Fatalf("rows = %d pending %d", total, pending)
	}
	want := []string{"open: Task opened", "advance: Moved on to Working", "pause: Waiting for approval"}
	if got := lines(t, store, ownerAccess, task.ID); !equalLines(got, want) {
		t.Fatalf("history = %q", got)
	}
}

func TestRequestApprovalRefusesASecondRequestWhilePending(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	task := reach(t, store, "File the receipts", domain.Working)
	request(t, store, task.ID)

	_, err := app.RequestApproval(context.Background(), store, ownerAccess, task.ID, "mail.send", "expired", "Send it again")
	if !errors.Is(err, domain.ErrPending) {
		t.Fatalf("second request = %v", err)
	}
	if total, pending := approvalRows(t, owner, task.ID); total != 1 || pending != 1 {
		t.Fatalf("rows = %d pending %d", total, pending)
	}
	if got := lines(t, store, ownerAccess, task.ID); len(got) != 3 {
		t.Fatalf("history = %q", got)
	}
}

func TestRequestApprovalIsAllOrNothing(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	planning := reach(t, store, "Plan it", domain.Planning)
	working := reach(t, store, "Do it", domain.Working)
	done := reach(t, store, "Done already", domain.Done)

	tests := []struct {
		name   string
		access app.Access
		task   string
		tool   string
		reason string
		want   error
	}{
		{"a task that is not Working", ownerAccess, planning.ID, "mail.send", "ask", domain.ErrInput},
		{"a finished task", ownerAccess, done.ID, "mail.send", "ask", domain.ErrTerminal},
		{"a credential in the tool", ownerAccess, working.ID, "send-Bearer-abc", "ask", domain.ErrApproval},
		{"an unknown reason", ownerAccess, working.ID, "mail.send", "always", domain.ErrApproval},
		{"a task nobody can see", otherAccess, working.ID, "mail.send", "ask", app.ErrGone},
		{"a shared editor who is not the owner", app.Access{
			UserID: otherUser, Allow: otherAccess.Allow, SharedView: "{" + working.ID + "}", SharedEdit: "{" + working.ID + "}",
		}, working.ID, "mail.send", "ask", app.ErrAccess},
	}
	for _, test := range tests {
		_, err := app.RequestApproval(ctx, store, test.access, test.task, test.tool, test.reason, askAction)
		if !errors.Is(err, test.want) {
			t.Errorf("%s: err = %v, want %v", test.name, err, test.want)
		}
	}
	var rows int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.approvals`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("approvals = %d %v", rows, err)
	}
	if got := taskStatus(t, owner, working.ID); got != string(domain.Working) {
		t.Fatalf("working task is %s", got)
	}
	if got := taskStatus(t, owner, planning.ID); got != string(domain.Planning) {
		t.Fatalf("planning task is %s", got)
	}
}

func TestApproveResumesTheTask(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	code, body := send(h, "owner", http.MethodPost, "/api/approvals/"+approval.ID+"/approve")
	if code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, body)
	}
	got := decode[approvalJSON](t, body)
	want := approvalJSON{ID: approval.ID, TaskID: task.ID, Title: "File the receipts", Action: askAction, State: "approved"}
	if got != want {
		t.Fatalf("approve = %+v, want %+v", got, want)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil || len(keys) != 5 {
		t.Fatalf("keys = %v %v", keys, err)
	}

	if status := taskStatus(t, owner, task.ID); status != string(domain.Working) {
		t.Fatalf("task is %s", status)
	}
	var decided bool
	if err := owner.QueryRow(context.Background(), `SELECT decided_at IS NOT NULL FROM tasks.approvals WHERE id = $1`, approval.ID).Scan(&decided); err != nil || !decided {
		t.Fatalf("decided_at set = %t %v", decided, err)
	}
	history := []string{
		"open: Task opened", "advance: Moved on to Working",
		"pause: Waiting for approval", "approval: Approval given",
	}
	if got := lines(t, store, ownerAccess, task.ID); !equalLines(got, history) {
		t.Fatalf("history = %q", got)
	}
	// The task is Working, so it can ask again.
	if _, err := app.RequestApproval(context.Background(), store, ownerAccess, task.ID, "mail.send", "ask", "Send the April invoice"); err != nil {
		t.Fatalf("a new request after the decision = %v", err)
	}
}

func TestDenyResumesTheTaskAndTellsTheAgent(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	code, body := send(h, "owner", http.MethodPost, "/api/approvals/"+approval.ID+"/deny")
	if code != http.StatusOK {
		t.Fatalf("deny = %d %s", code, body)
	}
	if got := decode[approvalJSON](t, body); got.State != "denied" || got.ID != approval.ID || got.Title != "File the receipts" {
		t.Fatalf("deny = %+v", got)
	}
	if status := taskStatus(t, owner, task.ID); status != string(domain.Working) {
		t.Fatalf("task is %s", status)
	}
	history := []string{
		"open: Task opened", "advance: Moved on to Working",
		"pause: Waiting for approval", "approval: Approval denied",
		"message: The action was refused. Find another way.",
	}
	if got := lines(t, store, ownerAccess, task.ID); !equalLines(got, history) {
		t.Fatalf("history = %q", got)
	}
}

func TestApproveAddsNoChatLine(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)
	if _, err := app.DecideApproval(context.Background(), store, ownerAccess, approval.ID, domain.Approve); err != nil {
		t.Fatal(err)
	}
	var chats int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, task.ID).Scan(&chats); err != nil || chats != 0 {
		t.Fatalf("chat lines = %d %v", chats, err)
	}
}

func TestASecondDecisionIs409AndChangesNothing(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	for _, first := range []string{"approve", "deny"} {
		for _, second := range []string{"approve", "deny"} {
			t.Run(first+" then "+second, func(t *testing.T) {
				task := reach(t, store, "File the receipts", domain.Working)
				approval := request(t, store, task.ID)
				path := "/api/approvals/" + approval.ID
				if code, body := send(h, "owner", http.MethodPost, path+"/"+first); code != http.StatusOK {
					t.Fatalf("%s = %d %s", first, code, body)
				}
				before := lines(t, store, ownerAccess, task.ID)

				code, body := send(h, "owner", http.MethodPost, path+"/"+second)
				if code != http.StatusConflict {
					t.Fatalf("%s again = %d %s", second, code, body)
				}
				if got := decode[map[string]string](t, body); got["error"] == "" || len(got) != 1 {
					t.Fatalf("body = %s", body)
				}
				if after := lines(t, store, ownerAccess, task.ID); !equalLines(after, before) {
					t.Fatalf("history changed:\n%q\n%q", before, after)
				}
				wantState := map[string]string{"approve": "approved", "deny": "denied"}[first]
				var state string
				if err := owner.QueryRow(context.Background(), `SELECT state FROM tasks.approvals WHERE id = $1`, approval.ID).Scan(&state); err != nil || state != wantState {
					t.Fatalf("state = %s %v, want %s", state, err, wantState)
				}
			})
		}
	}
}

func TestTwoDecisionsAtOnceSettleOnce(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, verb := range []string{"approve", "deny"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = send(h, "owner", http.MethodPost, "/api/approvals/"+approval.ID+"/"+verb)
		}()
	}
	wg.Wait()
	if !(codes[0] == http.StatusOK && codes[1] == http.StatusConflict) && !(codes[0] == http.StatusConflict && codes[1] == http.StatusOK) {
		t.Fatalf("codes = %v, want one 200 and one 409", codes)
	}
	var resumes int
	if err := owner.QueryRow(context.Background(), `
		SELECT count(*) FROM tasks.task_events WHERE task_id = $1 AND input IN ('approve', 'deny')`, task.ID).Scan(&resumes); err != nil || resumes != 1 {
		t.Fatalf("decision events = %d %v", resumes, err)
	}
	if status := taskStatus(t, owner, task.ID); status != string(domain.Working) {
		t.Fatalf("task is %s", status)
	}
}

func TestDecidingAClosedApprovalIs409AndChangesNothing(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)
	// The task fails while it waits, so the approval closes and the resume
	// cannot happen.
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Fail}); err != nil {
		t.Fatal(err)
	}
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})
	before := lines(t, store, ownerAccess, task.ID)

	for _, verb := range []string{"approve", "deny"} {
		code, body := send(h, "owner", http.MethodPost, "/api/approvals/"+approval.ID+"/"+verb)
		if code != http.StatusConflict || strings.TrimSpace(string(body)) != `{"error":"the task is no longer waiting"}` {
			t.Fatalf("%s a closed approval = %d %s", verb, code, body)
		}
	}
	var state string
	var decided bool
	if err := owner.QueryRow(ctx, `SELECT state, decided_at IS NOT NULL FROM tasks.approvals WHERE id = $1`, approval.ID).Scan(&state, &decided); err != nil || state != "closed" || !decided {
		t.Fatalf("the closed approval moved: %s %t %v", state, decided, err)
	}
	if after := lines(t, store, ownerAccess, task.ID); !equalLines(after, before) {
		t.Fatalf("history changed:\n%q\n%q", before, after)
	}
	var chats int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, task.ID).Scan(&chats); err != nil || chats != 0 {
		t.Fatalf("chat lines = %d %v", chats, err)
	}
}

func TestFailingATaskClosesItsPendingApproval(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	if code, body := send(h, "owner", http.MethodGet, "/api/approvals"); code != http.StatusOK || len(decode[[]approvalJSON](t, body)) != 1 {
		t.Fatalf("list before the fail = %d %s", code, body)
	}

	if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Fail}); err != nil {
		t.Fatal(err)
	}
	var state string
	var decided bool
	if err := owner.QueryRow(ctx, `SELECT state, decided_at IS NOT NULL FROM tasks.approvals WHERE id = $1`, approval.ID).Scan(&state, &decided); err != nil {
		t.Fatal(err)
	}
	if state != "closed" || !decided {
		t.Fatalf("the approval = %s decided %t", state, decided)
	}
	if code, body := send(h, "owner", http.MethodGet, "/api/approvals"); code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Fatalf("list after the fail = %d %s", code, body)
	}
}

func TestAParentCancelClosesTheChildrenPendingApprovals(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Send the invoice", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, child.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	approval := request(t, store, child.ID)
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	if _, err := app.Cancel(ctx, store, ownerAccess, parent.ID); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, owner, child.ID); got != string(domain.Failed) {
		t.Fatalf("child is %s", got)
	}
	var state string
	var decided bool
	if err := owner.QueryRow(ctx, `SELECT state, decided_at IS NOT NULL FROM tasks.approvals WHERE id = $1`, approval.ID).Scan(&state, &decided); err != nil {
		t.Fatal(err)
	}
	if state != "closed" || !decided {
		t.Fatalf("the approval = %s decided %t", state, decided)
	}
	if code, body := send(h, "owner", http.MethodGet, "/api/approvals"); code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Fatalf("list after the cancel = %d %s", code, body)
	}
}

func TestRunnerOwnedFailKeepsEditorStopAndSharedControls(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	approval := request(t, store, task.ID)
	editor := app.Access{
		UserID: otherUser, Allow: "task:view,task:edit",
		SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}",
	}
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	if _, err := app.ApplyTransition(ctx, store, otherAccess, task.ID, domain.Input{Kind: domain.Fail}); !errors.Is(err, app.ErrGone) {
		t.Fatalf("stranger Fail = %v, want ErrGone", err)
	}
	if got := taskStatus(t, owner, task.ID); got != string(domain.NeedsApproval) {
		t.Fatalf("stranger Fail changed task status to %s", got)
	}
	if _, err := app.ApplyTransition(ctx, store, editor, task.ID, domain.Input{Kind: domain.Fail}); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, owner, task.ID); got != string(domain.Failed) {
		t.Fatalf("task is %s", got)
	}
	var state string
	var decided bool
	if err := owner.QueryRow(ctx, `
		SELECT state, decided_at IS NOT NULL
		FROM tasks.approvals WHERE id = $1`, approval.ID).Scan(&state, &decided); err != nil {
		t.Fatal(err)
	}
	if state != "closed" || !decided {
		t.Fatalf("the approval = %s decided %t", state, decided)
	}
	if code, body := send(h, "owner", http.MethodGet, "/api/approvals"); code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Fatalf("list after the fail = %d %s", code, body)
	}
	manual := reach(t, store, "Shared manual fail", domain.Working)
	manualEditor := app.Access{UserID: otherUser, Allow: otherAccess.Allow,
		SharedView: "{" + manual.ID + "}", SharedEdit: "{" + manual.ID + "}"}
	if _, err := app.ApplyTransition(ctx, store, manualEditor, manual.ID, domain.Input{Kind: domain.Fail}); err != nil {
		t.Fatalf("shared editor Fail on non-runner-owned task = %v", err)
	}
}

func TestAParentCancelClosesAChildApprovalOwnedBySomeoneElse(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	spawner := app.Access{
		UserID: otherUser, Allow: "task:create,task:view,task:edit",
		SharedView: "{" + parent.ID + "}", SharedEdit: "{" + parent.ID + "}",
	}
	child, err := app.Spawn(ctx, store, spawner, parent.ID, "Send the invoice", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, spawner, child.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	approval, err := app.RequestApproval(ctx, store, spawner, child.ID, "mail.send", "ask", askAction)
	if err != nil {
		t.Fatal(err)
	}
	h := approvalServer(store, map[string]app.Access{"spawner": spawner})
	if code, body := send(h, "spawner", http.MethodGet, "/api/approvals"); code != http.StatusOK || len(decode[[]approvalJSON](t, body)) != 1 {
		t.Fatalf("list before the cancel = %d %s", code, body)
	}

	// The parent owner cancels. The child is shared back with them for edit,
	// which is what lets the cancel fail it.
	canceller := app.Access{
		UserID: ownerUser, Allow: ownerAccess.Allow,
		SharedView: "{" + child.ID + "}", SharedEdit: "{" + child.ID + "}",
	}
	if _, err := app.Cancel(ctx, store, canceller, parent.ID); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, owner, child.ID); got != string(domain.Failed) {
		t.Fatalf("child is %s", got)
	}
	var state string
	var decided bool
	if err := owner.QueryRow(ctx, `
		SELECT state, decided_at IS NOT NULL
		FROM tasks.approvals WHERE id = $1`, approval.ID).Scan(&state, &decided); err != nil {
		t.Fatal(err)
	}
	if state != "closed" || !decided {
		t.Fatalf("the approval = %s decided %t", state, decided)
	}
	if code, body := send(h, "spawner", http.MethodGet, "/api/approvals"); code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Fatalf("list after the cancel = %d %s", code, body)
	}
}

func TestAnEditorWhoFailedTheTaskStillListsAndDecidesNothing(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)
	editor := app.Access{
		UserID: otherUser, Allow: "task:view,task:edit",
		SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}",
	}
	h := approvalServer(store, map[string]app.Access{"editor": editor})

	// The editor can see the closed row. The list still hides it. A decision
	// is the same conflict the owner gets, and it changes nothing.
	if _, err := app.ApplyTransition(ctx, store, editor, task.ID, domain.Input{Kind: domain.Fail}); err != nil {
		t.Fatal(err)
	}
	if code, body := send(h, "editor", http.MethodGet, "/api/approvals"); code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Fatalf("editor list = %d %s", code, body)
	}
	for _, verb := range []string{"approve", "deny"} {
		code, body := send(h, "editor", http.MethodPost, "/api/approvals/"+approval.ID+"/"+verb)
		if code != http.StatusConflict || strings.TrimSpace(string(body)) != `{"error":"the task is no longer waiting"}` {
			t.Errorf("editor %s the closed approval = %d %s", verb, code, body)
		}
	}
}

func TestTwoRequestsAtOnceAskOnce(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	task := reach(t, store, "File the receipts", domain.Working)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = app.RequestApproval(context.Background(), store, ownerAccess, task.ID, "mail.send", "ask", askAction)
		}()
	}
	wg.Wait()
	refused := 0
	for _, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, domain.ErrPending) {
			t.Fatalf("a request = %v", err)
		}
		refused++
	}
	if refused != 1 {
		t.Fatalf("refusals = %d, want 1", refused)
	}
	if total, pending := approvalRows(t, owner, task.ID); total != 1 || pending != 1 {
		t.Fatalf("rows = %d pending %d", total, pending)
	}
	if got := taskStatus(t, owner, task.ID); got != string(domain.NeedsApproval) {
		t.Fatalf("task is %s", got)
	}
	if got := lines(t, store, ownerAccess, task.ID); len(got) != 3 {
		t.Fatalf("history = %q", got)
	}
}

func TestNobodyElseCanSeeOrDecideAnApproval(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)

	// A person who can view the shared task, and one who can also edit it.
	viewer := app.Access{UserID: otherUser, Allow: "task:view", SharedView: "{" + task.ID + "}"}
	editor := app.Access{
		UserID: otherUser, Allow: "task:view,task:edit",
		SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}",
	}
	h := approvalServer(store, map[string]app.Access{
		"owner": ownerAccess, "stranger": otherAccess, "viewer": viewer, "editor": editor,
	})

	for _, who := range []string{"stranger", "viewer"} {
		for _, verb := range []string{"approve", "deny"} {
			code, body := send(h, who, http.MethodPost, "/api/approvals/"+approval.ID+"/"+verb)
			if code != http.StatusNotFound || strings.TrimSpace(string(body)) != `{"error":"no such approval"}` {
				t.Errorf("%s %s = %d %s", who, verb, code, body)
			}
		}
		code, body := send(h, who, http.MethodGet, "/api/approvals")
		if code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
			t.Errorf("%s list = %d %s", who, code, body)
		}
	}
	// An editor with both shares can decide. The list still belongs to the owner.
	if code, body := send(h, "editor", http.MethodGet, "/api/approvals"); code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Errorf("editor list = %d %s", code, body)
	}
	// The shared task itself is readable, so the 404 is the approval's own rule.
	if _, err := app.ViewTask(ctx, store, viewer, task.ID); err != nil {
		t.Fatalf("viewer cannot see the task: %v", err)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "018f3b2c-7a10-7c4a-8e11-999999999999", "not-an-id"} {
		if code, body := send(h, "owner", http.MethodPost, "/api/approvals/"+id+"/approve"); code != http.StatusNotFound {
			t.Errorf("unknown id %s = %d %s", id, code, body)
		}
	}
	if status := taskStatus(t, owner, task.ID); status != string(domain.NeedsApproval) {
		t.Fatalf("task is %s", status)
	}
	if _, pending := approvalRows(t, owner, task.ID); pending != 1 {
		t.Fatalf("pending = %d", pending)
	}
}

func TestApprovalRoutesNeedASignedInPerson(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/approvals"},
		{http.MethodPost, "/api/approvals/" + approval.ID + "/approve"},
		{http.MethodPost, "/api/approvals/" + approval.ID + "/deny"},
	}
	for _, route := range routes {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		req.Header.Set("X-As", "owner")
		req.Header.Set("Authorization", "Bearer upmo_pat_abc")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != `{"error":"needs a signed-in person"}` {
			t.Errorf("bearer %s %s = %d %s", route.method, route.path, rec.Code, rec.Body.String())
		}
		if code, _ := send(h, "", route.method, route.path); code != http.StatusUnauthorized {
			t.Errorf("no session %s %s = %d", route.method, route.path, code)
		}
	}
	if _, pending := approvalRows(t, owner, task.ID); pending != 1 {
		t.Fatal("a refused request decided the approval")
	}
}

func TestPauseAndResumeStayClosedOverThePostRoute(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	task := reach(t, store, "File the receipts", domain.Working)
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	for _, kind := range []string{"pause", "resume", "approve", "deny"} {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID, strings.NewReader(`{"kind":"`+kind+`"}`))
		req.Header.Set("X-As", "owner")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("kind %s = %d %s", kind, rec.Code, rec.Body.String())
		}
	}
	if status := taskStatus(t, owner, task.ID); status != string(domain.Working) {
		t.Fatalf("task is %s", status)
	}
}

func TestListShowsPendingFirstThenTheMostRecentDecidedAndCapsAtFifty(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess, "stranger": otherAccess})

	if code, body := send(h, "owner", http.MethodGet, "/api/approvals"); code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Fatalf("empty list = %d %s", code, body)
	}

	// Two tasks ask and one is approved, then older decided rows fill the rest.
	decided := reach(t, store, "Decided first", domain.Working)
	decidedApproval := request(t, store, decided.ID)
	if _, err := app.DecideApproval(ctx, store, ownerAccess, decidedApproval.ID, domain.Approve); err != nil {
		t.Fatal(err)
	}
	older := reach(t, store, "Asked first", domain.Working)
	olderApproval := request(t, store, older.ID)
	newer := reach(t, store, "Asked last", domain.Working)
	newerApproval := request(t, store, newer.ID)
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.approvals (task_id, user_id, tool, reason, action, state, created_at, decided_at)
		SELECT $1::uuid, $2::uuid, 'mail.send', 'ask', 'Old action ' || n, 'denied',
		       now() - make_interval(mins => n + 1000), now() - make_interval(mins => n + 500)
		FROM generate_series(1, 60) AS n`, decided.ID, ownerUser); err != nil {
		t.Fatal(err)
	}
	// Closed rows are the most recently settled, so they would lead the list
	// and take cap slots if it held them.
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.approvals (task_id, user_id, tool, reason, action, state, created_at, decided_at)
		SELECT $1::uuid, $2::uuid, 'mail.send', 'ask', 'Closed action ' || n, 'closed',
		       now() - make_interval(mins => n), now()
		FROM generate_series(1, 5) AS n`, decided.ID, ownerUser); err != nil {
		t.Fatal(err)
	}

	code, body := send(h, "owner", http.MethodGet, "/api/approvals")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	got := decode[[]approvalJSON](t, body)
	if len(got) != 50 {
		t.Fatalf("list holds %d, want 50", len(got))
	}
	for _, item := range got {
		if item.State == "closed" {
			t.Fatalf("a closed row is listed: %+v", item)
		}
	}
	want := []approvalJSON{
		{newerApproval.ID, newer.ID, "Asked last", askAction, "pending"},
		{olderApproval.ID, older.ID, "Asked first", askAction, "pending"},
		{decidedApproval.ID, decided.ID, "Decided first", askAction, "approved"},
	}
	for i, item := range want {
		if got[i] != item {
			t.Errorf("item %d = %+v, want %+v", i, got[i], item)
		}
	}
	for i := 3; i < 50; i++ {
		if wantAction := fmt.Sprintf("Old action %d", i-2); got[i].Action != wantAction || got[i].State != "denied" || got[i].Title != "Decided first" {
			t.Fatalf("item %d = %+v, want %s", i, got[i], wantAction)
		}
	}
	if code, body := send(h, "stranger", http.MethodGet, "/api/approvals"); code != http.StatusOK || strings.TrimSpace(string(body)) != `[]` {
		t.Fatalf("stranger list = %d %s", code, body)
	}
}

func TestApprovalsHoldTheirRulesInTheDatabase(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)

	insert := `
		INSERT INTO tasks.approvals (task_id, user_id, tool, reason, action, state, decided_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::text::timestamptz)`
	now := "2026-01-01T00:00:00Z"
	tests := []struct {
		name string
		args []any
	}{
		{"a second pending row for the task", []any{task.ID, ownerUser, "t", "ask", "a", "pending", nil}},
		{"an empty action", []any{task.ID, ownerUser, "t", "ask", "", "approved", now}},
		{"a long action", []any{task.ID, ownerUser, "t", "ask", strings.Repeat("a", 201), "approved", now}},
		{"an unknown reason", []any{task.ID, ownerUser, "t", "always", "a", "approved", now}},
		{"an unknown state", []any{task.ID, ownerUser, "t", "ask", "a", "maybe", now}},
		{"a decided row with no decided_at", []any{task.ID, ownerUser, "t", "ask", "a", "approved", nil}},
		{"a closed row with no decided_at", []any{task.ID, ownerUser, "t", "ask", "a", "closed", nil}},
		{"a pending row with a decided_at", []any{task.ID, ownerUser, "t", "ask", "a", "pending", now}},
		{"no tool", []any{task.ID, ownerUser, "", "ask", "a", "approved", now}},
	}
	for _, test := range tests {
		if _, err := owner.Exec(ctx, insert, test.args...); err == nil {
			t.Errorf("accepted %s", test.name)
		}
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.approvals SET state = 'denied', decided_at = now() WHERE id = $1`, approval.ID); err != nil {
		t.Fatalf("a pending row cannot be decided: %v", err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.approvals SET state = 'approved' WHERE id = $1`, approval.ID); err == nil {
		t.Error("a decided row was decided again")
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.approvals SET state = 'pending', decided_at = NULL WHERE id = $1`, approval.ID); err == nil {
		t.Error("a decided row went back to pending")
	}
	// A closed row is settled the same way: it cannot be decided or reopened.
	if _, err := owner.Exec(ctx, insert, task.ID, ownerUser, "t", "ask", "a closed one", "closed", now); err != nil {
		t.Fatalf("a closed row cannot be stored: %v", err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.approvals SET state = 'approved' WHERE state = 'closed'`); err == nil {
		t.Error("a closed row was decided again")
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.approvals SET state = 'pending', decided_at = NULL WHERE state = 'closed'`); err == nil {
		t.Error("a closed row went back to pending")
	}
}

func TestAppRoleMayOnlySettleItsOwnApprovals(t *testing.T) {
	owner := up(t)
	pool := pgtest.App(t, owner)
	store := postgres.Store{Pool: pool}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Working)
	approval := request(t, store, task.ID)

	as := func(user, allow, statement string, args ...any) (int64, error) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`, user, allow); err != nil {
			t.Fatal(err)
		}
		tag, err := tx.Exec(ctx, statement, args...)
		return tag.RowsAffected(), err
	}
	for _, column := range []string{"tool", "action", "reason", "task_id", "user_id", "created_at"} {
		if _, err := as(ownerUser, ownerAccess.Allow, fmt.Sprintf(`UPDATE tasks.approvals SET %[1]s = %[1]s`, column)); err == nil {
			t.Errorf("the app role updated %s", column)
		}
	}
	if _, err := as(ownerUser, ownerAccess.Allow, `DELETE FROM tasks.approvals`); err == nil {
		t.Error("the app role deleted an approval")
	}
	settle := `UPDATE tasks.approvals SET state = 'approved', decided_at = now() WHERE id = $1`
	if n, err := as(otherUser, otherAccess.Allow, settle, approval.ID); err != nil || n != 0 {
		t.Errorf("another person settled it: %d rows, %v", n, err)
	}
	if n, err := as(ownerUser, "task:view", settle, approval.ID); err != nil || n != 0 {
		t.Errorf("an owner with no task:edit settled it: %d rows, %v", n, err)
	}
	// A row cannot be stored for someone else, nor for a task the user does not own.
	insert := `INSERT INTO tasks.approvals (task_id, user_id, tool, reason, action) VALUES ($1::uuid, $2::uuid, 't', 'ask', 'a')`
	if _, err := as(otherUser, otherAccess.Allow, insert, task.ID, otherUser); err == nil {
		t.Error("a person stored an approval for a task they do not own")
	}
	if _, err := as(otherUser, otherAccess.Allow, insert, task.ID, ownerUser); err == nil {
		t.Error("a person stored an approval for someone else")
	}
	if n, err := as(ownerUser, ownerAccess.Allow, settle, approval.ID); err != nil || n != 1 {
		t.Errorf("the owner settling their own: %d rows, %v", n, err)
	}
}
