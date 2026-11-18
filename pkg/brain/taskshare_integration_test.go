//go:build integration

package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	shareGhostPerson = "018f3b2c-7a10-7c4a-8e11-999999999999"
	shareGhostTeam   = "018f3b2c-7a10-7c4a-8e11-888888888888"
)

// shareMemberWire is one member of a task share on the wire.
type shareMemberWire struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Name        string `json:"name,omitempty"`
	Role        string `json:"role"`
}

// shareWire is a task share on the wire, as GET answers and PUT takes it.
type shareWire struct {
	Visibility string            `json:"visibility"`
	TeamID     string            `json:"team_id,omitempty"`
	Members    []shareMemberWire `json:"members"`
}

// shareEnv is a real identity and tasks schema behind the real task routes.
// The X-As header names who is signed in; no header means no session.
type shareEnv struct {
	owner   *pgxpool.Pool
	handler http.Handler
	people  map[string]string
	teams   map[string]string
}

// shareSetup seeds Ada (an owner, in Design and Build), Bob, Cy and Dee (no
// team), Eve (Design only), Gus (Build only), a disabled Ex and an empty Ops.
func shareSetup(t *testing.T) *shareEnv {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	// The task store records audit events, so the audit schema comes up first.
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	members := identity.Members(pool, nil, "", nil, nil)
	store := tasks.NewStore(pool, nil)
	store.ReplaceTaskGrants = taskGrants{members: members}.ReplaceTaskGrantsTx
	mux := http.NewServeMux()
	tasks.Handler{
		Book:   store,
		List:   store,
		View:   store,
		Shares: store,
		Grants: taskGrants{members: members},
		Access: func(r *http.Request) (tasks.Access, bool) {
			person, err := identity.ParseUserID(r.Header.Get("X-As"))
			if err != nil {
				return tasks.Access{}, false
			}
			allow, err := members.TaskAllow(r.Context(), person)
			if err != nil || allow == "" {
				return tasks.Access{}, false
			}
			shares, err := members.ShareAccess(r.Context(), person, identity.TypeTask)
			if err != nil {
				return tasks.Access{}, false
			}
			return tasks.Access{
				UserID:     person.String(),
				Allow:      allow,
				Teams:      shares.Teams,
				SharedView: shares.SharedView,
				SharedEdit: shares.SharedEdit,
			}, true
		},
	}.Mount(mux, nil)

	env := &shareEnv{owner: owner, handler: mux, people: map[string]string{}, teams: map[string]string{}}
	env.person(t, "Ada", "owner", false)
	for _, name := range []string{"Bob", "Cy", "Dee", "Eve", "Gus"} {
		env.person(t, name, "member", false)
	}
	env.person(t, "Ex", "member", true)
	env.team(t, "Design", "ada", "eve")
	env.team(t, "Build", "ada", "gus")
	env.team(t, "Ops")
	return env
}

func (e *shareEnv) person(t *testing.T, name, role string, disabled bool) {
	t.Helper()
	var id string
	err := e.owner.QueryRow(context.Background(), `
		INSERT INTO identity.users (email, name, role_id, joined_at, disabled_at)
		SELECT lower($1::text) || '@example.com', $1::text, role.id, now(),
		       CASE WHEN $3::boolean THEN now() END
		FROM identity.roles AS role
		WHERE role.key = $2::text
		RETURNING id::text`, name, role, disabled).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	e.people[strings.ToLower(name)] = id
}

func (e *shareEnv) team(t *testing.T, name string, members ...string) {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := e.owner.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.teams[strings.ToLower(name)] = id
	for _, member := range members {
		if _, err := e.owner.Exec(ctx, `
			INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, id, e.people[member]); err != nil {
			t.Fatal(err)
		}
	}
}

// roleGrant stores a grant to a whole role, which a share never lists or changes.
func (e *shareEnv) roleGrant(t *testing.T, task string) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO identity.shares (object_type, object_id, grantee_role_id, level, granted_by)
		SELECT 'task', $1::uuid, role.id, 'viewer', $2::uuid
		FROM identity.roles AS role
		WHERE role.key = 'viewer'`, task, e.people["ada"]); err != nil {
		t.Fatal(err)
	}
}

func (e *shareEnv) call(as, method, path, body string) (int, []byte) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if as != "" {
		req.Header.Set("X-As", e.people[as])
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func shareDecode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return out
}

func shareURL(task string) string { return "/api/tasks/" + task + "/share" }

// entry is one member of a PUT body: a person or a team, by seeded name.
func (e *shareEnv) entry(kind, name, role string) shareMemberWire {
	id := e.people[name]
	if kind == "team" {
		id = e.teams[name]
	}
	return shareMemberWire{SubjectType: kind, SubjectID: id, Role: role}
}

func (e *shareEnv) put(t *testing.T, as, task string, share shareWire) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(share)
	if err != nil {
		t.Fatal(err)
	}
	return e.call(as, http.MethodPut, shareURL(task), string(body))
}

func (e *shareEnv) mustPut(t *testing.T, as, task string, share shareWire) shareWire {
	t.Helper()
	code, body := e.put(t, as, task, share)
	if code != http.StatusOK {
		t.Fatalf("put as %s = %d %s", as, code, body)
	}
	return shareDecode[shareWire](t, body)
}

// openTask opens a task as a person and moves it to Working, where it takes chat.
func (e *shareEnv) openTask(t *testing.T, as, goal string) string {
	t.Helper()
	code, body := e.call(as, http.MethodPost, "/api/tasks", `{"goal":"`+goal+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("open = %d %s", code, body)
	}
	id := shareDecode[map[string]string](t, body)["id"]
	if code, body := e.call(as, http.MethodPost, "/api/tasks/"+id, `{"kind":"advance"}`); code != http.StatusOK {
		t.Fatalf("advance = %d %s", code, body)
	}
	return id
}

func (e *shareEnv) sees(t *testing.T, as, task string) bool {
	t.Helper()
	code, body := e.call(as, http.MethodGet, "/api/tasks", "")
	if code != http.StatusOK {
		t.Fatalf("list as %s = %d %s", as, code, body)
	}
	for _, row := range shareDecode[[]struct {
		ID string `json:"id"`
	}](t, body) {
		if row.ID == task {
			return true
		}
	}
	return false
}

func (e *shareEnv) chat(as, task, text string) int {
	code, _ := e.call(as, http.MethodPost, "/api/tasks/"+task, `{"kind":"chat","body":"`+text+`"}`)
	return code
}

func (e *shareEnv) chatLines(t *testing.T, task string) int {
	t.Helper()
	var lines int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM tasks.chat_events WHERE task_id = $1::uuid`, task).Scan(&lines); err != nil {
		t.Fatal(err)
	}
	return lines
}

// state is what a share stores for a task: its person, team and role grants
// with their levels, and the task's visibility with its team.
func (e *shareEnv) state(t *testing.T, task string) string {
	t.Helper()
	var grants, visibility string
	ctx := context.Background()
	if err := e.owner.QueryRow(ctx, `
		SELECT COALESCE(string_agg(id::text || ':' || level, ',' ORDER BY id), '')
		FROM identity.shares
		WHERE object_type = 'task' AND object_id = $1::uuid`, task).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(ctx, `
		SELECT visibility || ':' || COALESCE(visibility_team_id::text, '')
		FROM tasks.tasks WHERE id = $1::uuid`, task).Scan(&visibility); err != nil {
		t.Fatal(err)
	}
	return grants + "|" + visibility
}

func shareHas(members []shareMemberWire, want shareMemberWire) bool {
	for _, member := range members {
		if member == want {
			return true
		}
	}
	return false
}

func TestTaskShareReadIsForTheOwnerAndPeopleWhoCanEdit(t *testing.T) {
	e := shareSetup(t)
	task := e.openTask(t, "ada", "Close the books")
	e.mustPut(t, "ada", task, shareWire{Visibility: "private", Members: []shareMemberWire{
		e.entry("person", "ada", "owner"),
		e.entry("person", "bob", "editor"),
		e.entry("person", "cy", "viewer"),
		e.entry("team", "design", "viewer"),
	}})
	e.roleGrant(t, task)

	code, body := e.call("ada", http.MethodGet, shareURL(task), "")
	if code != http.StatusOK {
		t.Fatalf("owner get = %d %s", code, body)
	}
	if keys := shareDecode[map[string]json.RawMessage](t, body); keys["team_id"] != nil {
		t.Fatalf("a private share names a team: %s", body)
	}
	got := shareDecode[shareWire](t, body)
	if got.Visibility != "private" || got.TeamID != "" || len(got.Members) != 4 {
		t.Fatalf("share = %+v", got)
	}
	if want := (shareMemberWire{SubjectType: "person", SubjectID: e.people["ada"], Name: "Ada", Role: "owner"}); got.Members[0] != want {
		t.Fatalf("first member = %+v, want the owner %+v", got.Members[0], want)
	}
	for _, want := range []shareMemberWire{
		{SubjectType: "person", SubjectID: e.people["bob"], Name: "Bob", Role: "editor"},
		{SubjectType: "person", SubjectID: e.people["cy"], Name: "Cy", Role: "viewer"},
		{SubjectType: "team", SubjectID: e.teams["design"], Name: "Design", Role: "viewer"},
	} {
		if !shareHas(got.Members, want) {
			t.Errorf("share lacks %+v: %+v", want, got.Members)
		}
	}

	if code, body := e.call("bob", http.MethodGet, shareURL(task), ""); code != http.StatusOK {
		t.Fatalf("editor get = %d %s", code, body)
	}
	code, body = e.call("cy", http.MethodGet, shareURL(task), "")
	if refusal := shareDecode[map[string]string](t, body); code != http.StatusForbidden || len(refusal) != 1 || refusal["error"] == "" {
		t.Fatalf("viewer get = %d %s", code, body)
	}
	if code, body := e.call("dee", http.MethodGet, shareURL(task), ""); code != http.StatusNotFound {
		t.Fatalf("stranger get = %d %s", code, body)
	}
	if code, body := e.call("", http.MethodGet, shareURL(task), ""); code != http.StatusUnauthorized {
		t.Fatalf("no session get = %d %s", code, body)
	}
	req := httptest.NewRequest(http.MethodGet, shareURL(task), nil)
	req.Header.Set("X-As", e.people["ada"])
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bearer get = %d %s", rec.Code, rec.Body.String())
	}
}

func TestTaskSharePutReplacesTheGrants(t *testing.T) {
	e := shareSetup(t)
	ctx := context.Background()
	task := e.openTask(t, "ada", "Close the books")
	e.mustPut(t, "ada", task, shareWire{Visibility: "private", Members: []shareMemberWire{
		e.entry("person", "ada", "owner"),
		e.entry("person", "bob", "editor"),
		e.entry("person", "cy", "viewer"),
		e.entry("team", "design", "viewer"),
	}})
	e.roleGrant(t, task)

	replacement := shareWire{Visibility: "private", Members: []shareMemberWire{
		e.entry("person", "ada", "owner"),
		e.entry("person", "bob", "viewer"),
		e.entry("team", "build", "editor"),
	}}
	before := e.state(t, task)
	if code, body := e.put(t, "bob", task, replacement); code != http.StatusForbidden {
		t.Fatalf("editor put = %d %s", code, body)
	}
	if code, body := e.put(t, "dee", task, replacement); code != http.StatusNotFound {
		t.Fatalf("stranger put = %d %s", code, body)
	}
	if after := e.state(t, task); after != before {
		t.Fatalf("a refused put changed the share:\n%s\n%s", before, after)
	}

	got := e.mustPut(t, "ada", task, replacement)
	if len(got.Members) != 3 || got.Members[0].Role != "owner" || got.Members[0].SubjectID != e.people["ada"] {
		t.Fatalf("stored share = %+v", got)
	}
	for _, want := range []shareMemberWire{
		{SubjectType: "person", SubjectID: e.people["bob"], Name: "Bob", Role: "viewer"},
		{SubjectType: "team", SubjectID: e.teams["build"], Name: "Build", Role: "editor"},
	} {
		if !shareHas(got.Members, want) {
			t.Errorf("stored share lacks %+v: %+v", want, got.Members)
		}
	}
	var dropped, byRole int
	var grantedBy string
	if err := e.owner.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE user_id = $2::uuid OR team_id = $3::uuid),
		       count(*) FILTER (WHERE grantee_role_id IS NOT NULL),
		       COALESCE(max(granted_by::text) FILTER (WHERE team_id = $4::uuid), '')
		FROM identity.shares
		WHERE object_type = 'task' AND object_id = $1::uuid`,
		task, e.people["cy"], e.teams["design"], e.teams["build"]).Scan(&dropped, &byRole, &grantedBy); err != nil {
		t.Fatal(err)
	}
	if dropped != 0 || byRole != 1 || grantedBy != e.people["ada"] {
		t.Fatalf("dropped %d role grants %d granted_by %q", dropped, byRole, grantedBy)
	}
}

func TestTaskSharePutRefusals(t *testing.T) {
	e := shareSetup(t)
	adaTask := e.openTask(t, "ada", "Close the books")
	deeTask := e.openTask(t, "dee", "Sort the mail")
	e.mustPut(t, "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
		e.entry("person", "ada", "owner"),
		e.entry("person", "bob", "editor"),
	}})

	owner := e.entry("person", "ada", "owner")
	tests := []struct {
		name string
		as   string
		task string
		body shareWire
		text string
	}{
		{"unknown visibility", "ada", adaTask, shareWire{Visibility: "secret", Members: []shareMemberWire{owner}}, "unknown visibility"},
		{"a person who is not a member", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			owner, {SubjectType: "person", SubjectID: shareGhostPerson, Role: "editor"},
		}}, "that person is not a member"},
		{"a disabled person", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			owner, e.entry("person", "ex", "viewer"),
		}}, "that person is not a member"},
		{"a team that does not exist", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			owner, {SubjectType: "team", SubjectID: shareGhostTeam, Role: "viewer"},
		}}, "that team does not exist"},
		{"the same person twice", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			owner, e.entry("person", "bob", "editor"), e.entry("person", "bob", "viewer"),
		}}, "that access is listed twice"},
		{"the owner removed", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			e.entry("person", "bob", "editor"),
		}}, "the owner cannot be removed"},
		{"the owner listed twice", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			owner, owner,
		}}, "that access is listed twice"},
		{"the owner role changed", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			e.entry("person", "ada", "editor"),
		}}, "the owner cannot be changed"},
		{"the owner role for someone else", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			owner, e.entry("person", "bob", "owner"),
		}}, "a role must be editor or viewer"},
		{"an unknown role", "ada", adaTask, shareWire{Visibility: "private", Members: []shareMemberWire{
			owner, e.entry("person", "bob", "chief"),
		}}, "a role must be editor or viewer"},
		{"team visibility with several teams and none named", "ada", adaTask, shareWire{Visibility: "team", Members: []shareMemberWire{
			owner,
		}}, "the owner belongs to more than one team"},
		{"team visibility for a team the owner is not on", "ada", adaTask, shareWire{Visibility: "team", TeamID: e.teams["ops"], Members: []shareMemberWire{
			owner,
		}}, "the owner does not belong to that team"},
		{"team visibility with a team id that is not an id", "ada", adaTask, shareWire{Visibility: "team", TeamID: "build", Members: []shareMemberWire{
			owner,
		}}, "that team does not exist"},
		{"team visibility for an owner with no team", "dee", deeTask, shareWire{Visibility: "team", Members: []shareMemberWire{
			e.entry("person", "dee", "owner"),
		}}, "the owner belongs to no team"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := e.state(t, test.task)
			code, body := e.put(t, test.as, test.task, test.body)
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d %s", code, body)
			}
			if got := shareDecode[map[string]string](t, body); len(got) != 1 || got["error"] != test.text {
				t.Fatalf("body = %s, want error %q", body, test.text)
			}
			if after := e.state(t, test.task); after != before {
				t.Fatalf("a refused put changed the share:\n%s\n%s", before, after)
			}
		})
	}
}

func TestTaskShareTeamVisibility(t *testing.T) {
	e := shareSetup(t)
	ctx := context.Background()

	eveTask := e.openTask(t, "eve", "Plan the offsite")
	onlyOwner := func(name string) []shareMemberWire { return []shareMemberWire{e.entry("person", name, "owner")} }
	got := e.mustPut(t, "eve", eveTask, shareWire{Visibility: "team", Members: onlyOwner("eve")})
	if got.Visibility != "team" || got.TeamID != e.teams["design"] {
		t.Fatalf("the owner's only team is the default: %+v", got)
	}
	if !e.sees(t, "ada", eveTask) || e.sees(t, "dee", eveTask) || e.sees(t, "gus", eveTask) {
		t.Fatal("team visibility reaches the team and no one else")
	}

	got = e.mustPut(t, "eve", eveTask, shareWire{Visibility: "everyone", TeamID: e.teams["design"], Members: onlyOwner("eve")})
	if got.Visibility != "everyone" || got.TeamID != "" {
		t.Fatalf("everyone names no team: %+v", got)
	}
	var noTeam bool
	if err := e.owner.QueryRow(ctx, `SELECT visibility_team_id IS NULL FROM tasks.tasks WHERE id = $1::uuid`, eveTask).Scan(&noTeam); err != nil || !noTeam {
		t.Fatalf("stored team is null = %t %v", noTeam, err)
	}
	if !e.sees(t, "dee", eveTask) {
		t.Fatal("everyone reaches a person on no team")
	}

	got = e.mustPut(t, "eve", eveTask, shareWire{Visibility: "private", Members: onlyOwner("eve")})
	if got.Visibility != "private" || got.TeamID != "" || e.sees(t, "dee", eveTask) {
		t.Fatalf("private reaches no one: %+v", got)
	}

	adaTask := e.openTask(t, "ada", "Close the books")
	got = e.mustPut(t, "ada", adaTask, shareWire{Visibility: "team", TeamID: e.teams["build"], Members: onlyOwner("ada")})
	if got.TeamID != e.teams["build"] {
		t.Fatalf("the named team is stored: %+v", got)
	}
	if !e.sees(t, "gus", adaTask) || e.sees(t, "eve", adaTask) {
		t.Fatal("the named team sees the task and the owner's other team does not")
	}
}

func TestTaskShareGrantsAndRemovesAccess(t *testing.T) {
	e := shareSetup(t)
	task := e.openTask(t, "ada", "Close the books")

	if e.sees(t, "bob", task) || e.chat("bob", task, "hello") == http.StatusCreated {
		t.Fatal("a person with no grant reached the task")
	}

	e.mustPut(t, "ada", task, shareWire{Visibility: "private", Members: []shareMemberWire{
		e.entry("person", "ada", "owner"),
		e.entry("person", "bob", "editor"),
		e.entry("person", "cy", "viewer"),
	}})
	for _, name := range []string{"bob", "cy"} {
		if !e.sees(t, name, task) {
			t.Errorf("%s does not see the task in the list", name)
		}
	}
	if e.sees(t, "dee", task) {
		t.Error("a person with no grant sees the task in the list")
	}
	if code := e.chat("bob", task, "from the editor"); code != http.StatusCreated {
		t.Fatalf("editor chat = %d", code)
	}
	if code := e.chat("cy", task, "from the viewer"); code == http.StatusCreated {
		t.Fatalf("viewer chat = %d", code)
	}
	if lines := e.chatLines(t, task); lines != 1 {
		t.Fatalf("chat lines = %d, want only the editor's", lines)
	}

	e.mustPut(t, "ada", task, shareWire{Visibility: "private", Members: []shareMemberWire{e.entry("person", "ada", "owner")}})
	for _, name := range []string{"bob", "cy"} {
		if e.sees(t, name, task) {
			t.Errorf("%s still sees the task after the grant was removed", name)
		}
	}
	if code := e.chat("bob", task, "after removal"); code == http.StatusCreated {
		t.Fatalf("chat after removal = %d", code)
	}
	if lines := e.chatLines(t, task); lines != 1 {
		t.Fatalf("chat lines = %d after removal", lines)
	}
}

func TestTaskSharePutTwiceGivesTheSameResult(t *testing.T) {
	e := shareSetup(t)
	task := e.openTask(t, "ada", "Close the books")
	share := shareWire{Visibility: "team", TeamID: e.teams["design"], Members: []shareMemberWire{
		e.entry("person", "ada", "owner"),
		e.entry("person", "bob", "editor"),
		e.entry("team", "build", "viewer"),
	}}

	code, first := e.put(t, "ada", task, share)
	if code != http.StatusOK {
		t.Fatalf("first put = %d %s", code, first)
	}
	stored := e.state(t, task)
	code, second := e.put(t, "ada", task, share)
	if code != http.StatusOK {
		t.Fatalf("second put = %d %s", code, second)
	}
	if string(first) != string(second) {
		t.Fatalf("answers differ:\n%s\n%s", first, second)
	}
	if again := e.state(t, task); again != stored {
		t.Fatalf("the second put changed the stored share:\n%s\n%s", stored, again)
	}
}

func TestTaskSharePutConcurrentlyConverges(t *testing.T) {
	e := shareSetup(t)
	ctx := context.Background()
	task := e.openTask(t, "ada", "Close the books")
	share := shareWire{Visibility: "private", Members: []shareMemberWire{
		e.entry("person", "ada", "owner"),
		e.entry("person", "bob", "editor"),
	}}
	body, err := json.Marshal(share)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := e.owner.QueryRow(ctx, `
		SELECT count(*) FROM identity.shares
		WHERE object_type = 'task' AND object_id = $1::uuid`, task).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("the task already has %d grant rows", rows)
	}

	start := make(chan struct{})
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i], _ = e.call("ada", http.MethodPut, shareURL(task), string(body))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("put %d = %d, want both to converge on 200", i, code)
		}
	}
	if err := e.owner.QueryRow(ctx, `
		SELECT count(*) FROM identity.shares
		WHERE object_type = 'task' AND object_id = $1::uuid AND user_id = $2::uuid`,
		task, e.people["bob"]).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the share stores %d grants for Bob, want exactly one", rows)
	}
}

func TestTaskSharePutGuards(t *testing.T) {
	e := shareSetup(t)
	task := e.openTask(t, "ada", "Close the books")
	owner := e.entry("person", "ada", "owner")
	e.mustPut(t, "ada", task, shareWire{Visibility: "private", Members: []shareMemberWire{
		owner,
		e.entry("person", "cy", "viewer"),
	}})
	replacement := shareWire{Visibility: "private", Members: []shareMemberWire{owner}}
	before := e.state(t, task)

	if code, body := e.put(t, "cy", task, replacement); code != http.StatusForbidden {
		t.Fatalf("viewer put = %d %s", code, body)
	}
	valid, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, shareURL(task), strings.NewReader(string(valid)))
	req.Header.Set("X-As", e.people["ada"])
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bearer put = %d %s", rec.Code, rec.Body.String())
	}
	if code, body := e.call("ada", http.MethodPut, shareURL(task), `{"visibility":`); code != http.StatusBadRequest {
		t.Fatalf("broken body put = %d %s", code, body)
	}
	robot := shareWire{Visibility: "private", Members: []shareMemberWire{
		owner, {SubjectType: "robot", SubjectID: e.people["bob"], Role: "editor"},
	}}
	code, body := e.put(t, "ada", task, robot)
	if refusal := shareDecode[map[string]string](t, body); code != http.StatusUnprocessableEntity || len(refusal) != 1 || refusal["error"] != "share with a person or a team" {
		t.Fatalf("robot put = %d %s", code, body)
	}
	if after := e.state(t, task); after != before {
		t.Fatalf("a refused put changed the share:\n%s\n%s", before, after)
	}
}

func TestTaskSharePutTakesATeamIDInCapitals(t *testing.T) {
	e := shareSetup(t)
	task := e.openTask(t, "ada", "Close the books")
	got := e.mustPut(t, "ada", task, shareWire{
		Visibility: "team",
		TeamID:     strings.ToUpper(e.teams["build"]),
		Members:    []shareMemberWire{e.entry("person", "ada", "owner")},
	})
	if got.TeamID != e.teams["build"] {
		t.Fatalf("stored team = %q, want the canonical %q", got.TeamID, e.teams["build"])
	}
}
