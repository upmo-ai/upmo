//go:build integration

package identity_test

import (
	"context"
	"crypto/rand"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// actorDefiners are the definers bound to the actor's session token hash,
// with their argument types.
var actorDefiners = []string{
	"identity.names_of(bytea, uuid[], uuid[])",
	"identity.role_directory(bytea)",
	"identity.object_grants(bytea, text, uuid)",
	"identity.replace_object_grants(bytea, text, uuid, jsonb)",
	"identity.add_team_member(bytea, uuid, uuid)",
	"identity.remove_team_member(bytea, uuid, uuid)",
	"identity.team_create(bytea, text)",
}

// definerWorld is the cast the actor definer tests share: a joined owner
// with a live session, and the sessions that must be refused.
type definerWorld struct {
	owner, app *pgxpool.Pool
	ctx        context.Context
	ownerID    string
	ownerSess  []byte
	// bad are the hashes no definer may accept, by name.
	bad map[string][]byte
}

func newDefinerWorld(t *testing.T) *definerWorld {
	t.Helper()
	owner, ctx := upOwner(t)
	w := &definerWorld{owner: owner, app: pgtest.AppLogin(t, owner), ctx: ctx}
	ownerRole := w.roleID(t, "owner")
	adminRole := w.roleID(t, "admin")
	w.ownerID = w.person(t, "ada@example.com", "Ada", ownerRole, true, false)
	disabledID := w.person(t, "gone@example.com", "Gone", adminRole, true, true)
	unjoinedID := w.person(t, "new@example.com", "New", adminRole, false, false)

	now := time.Now()
	w.ownerSess = randomHash(t)
	seedSess(t, owner, ctx, w.ownerID, w.ownerSess, now.Add(time.Hour))
	expired, disabled, unjoined := randomHash(t), randomHash(t), randomHash(t)
	seedSess(t, owner, ctx, w.ownerID, expired, now.Add(-time.Minute))
	seedSess(t, owner, ctx, disabledID, disabled, now.Add(time.Hour))
	seedSess(t, owner, ctx, unjoinedID, unjoined, now.Add(time.Hour))
	w.bad = map[string][]byte{
		"null":     nil,
		"random":   randomHash(t),
		"expired":  expired,
		"disabled": disabled,
		"unjoined": unjoined,
	}
	return w
}

func (w *definerWorld) roleID(t *testing.T, key string) string {
	t.Helper()
	var id string
	if err := w.owner.QueryRow(w.ctx, `SELECT id::text FROM identity.roles WHERE key = $1`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// person seeds a person with a display name and returns their id.
func (w *definerWorld) person(t *testing.T, email, name, roleID string, joined, disabled bool) string {
	t.Helper()
	id := seedPerson(t, w.owner, w.ctx, email, roleID, joined, disabled)
	if _, err := w.owner.Exec(w.ctx, `UPDATE identity.users SET name = $2 WHERE id = $1::uuid`, id, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func (w *definerWorld) team(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := w.owner.QueryRow(w.ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (w *definerWorld) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func randomHash(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (w *definerWorld) randomUUID(t *testing.T) string {
	t.Helper()
	var id string
	if err := w.owner.QueryRow(w.ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Every actor definer refuses a hash with no live joined session, even when
// upmo.user_id names the owner, and changes nothing.
func TestActorDefinersRefuseABadHash(t *testing.T) {
	w := newDefinerWorld(t)
	team := w.team(t, "T1")
	task := w.randomUUID(t)
	grants := `[{"subject_type":"person","subject_id":"` + w.ownerID + `","level":"editor"}]`
	calls := map[string]struct {
		sql  string
		args []any
	}{
		"names_of":              {`SELECT * FROM identity.names_of($1, $2::uuid[], $3::uuid[])`, []any{[]string{w.ownerID}, []string{team}}},
		"role_directory":        {`SELECT * FROM identity.role_directory($1)`, nil},
		"object_grants":         {`SELECT * FROM identity.object_grants($1, 'task', $2::uuid)`, []any{task}},
		"replace_object_grants": {`SELECT * FROM identity.replace_object_grants($1, 'task', $2::uuid, $3::jsonb)`, []any{task, grants}},
		"add_team_member":       {`SELECT * FROM identity.add_team_member($1, $2::uuid, $3::uuid)`, []any{team, w.ownerID}},
		"remove_team_member":    {`SELECT * FROM identity.remove_team_member($1, $2::uuid, $3::uuid)`, []any{team, w.ownerID}},
		"team_create":           {`SELECT * FROM identity.team_create($1, $2)`, []any{"T2"}},
	}
	if len(calls) != len(actorDefiners) {
		t.Fatalf("calls cover %d definers, want all %d", len(calls), len(actorDefiners))
	}
	shares, members := w.count(t, "identity.shares"), w.count(t, "identity.team_members")
	for fn, call := range calls {
		for name, hash := range w.bad {
			tx, err := w.app.Begin(w.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(w.ctx, `SELECT set_config('upmo.user_id', $1, true)`, w.ownerID); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(w.ctx, call.sql, append([]any{hash}, call.args...)...)
			_ = tx.Rollback(w.ctx)
			assertDenied(t, fn+" "+name, err)
		}
	}
	if got := w.count(t, "identity.shares"); got != shares {
		t.Fatalf("shares = %d, want %d unchanged", got, shares)
	}
	if got := w.count(t, "identity.team_members"); got != members {
		t.Fatalf("team_members = %d, want %d unchanged", got, members)
	}
}

func TestTeamCreateDefinerValidatesActorAndName(t *testing.T) {
	w := newDefinerWorld(t)
	store := postgres.Passkeys{Pool: w.app}
	events := w.count(t, "audit.pending WHERE action = 'team.create'")

	for name, hash := range w.bad {
		_, err := w.app.Exec(w.ctx, `SELECT * FROM identity.team_create($1, 'Bad')`, hash)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" || pgErr.Message != "identity: invalid actor session" {
			t.Fatalf("team_create %s = %v, want SQLSTATE 42501 identity: invalid actor session", name, err)
		}
		if _, err := store.CreateTeam(w.ctx, hash, "Bad"); !errors.Is(err, app.ErrNotSignedIn) {
			t.Fatalf("CreateTeam with %s hash = %v, want ErrNotSignedIn", name, err)
		}
	}

	for _, name := range []string{"", "   ", "\t", "\n", "\u00a0"} {
		_, err := w.app.Exec(w.ctx, `SELECT * FROM identity.team_create($1, $2)`, w.ownerSess, name)
		if pgCode(err) != "22023" {
			t.Fatalf("team_create(%q) = %v, want SQLSTATE 22023", name, err)
		}
		if _, err := store.CreateTeam(w.ctx, w.ownerSess, name); !errors.Is(err, domain.ErrBadTeam) {
			t.Fatalf("CreateTeam(%q) = %v, want ErrBadTeam", name, err)
		}
	}
	if got := w.count(t, "audit.pending WHERE action = 'team.create'"); got != events {
		t.Fatalf("team.create events after refused creates = %d, want %d", got, events)
	}
	if _, err := w.app.Exec(w.ctx, `SELECT * FROM identity.team_create($1, NULL)`, w.ownerSess); pgCode(err) != "22023" {
		t.Fatalf("team_create(NULL) = %v, want SQLSTATE 22023", err)
	}
	if _, err := store.CreateTeam(w.ctx, w.ownerSess, strings.Repeat("x", 65)); !errors.Is(err, domain.ErrBadTeam) {
		t.Fatalf("CreateTeam(65 runes) = %v, want ErrBadTeam", err)
	}
	if _, err := w.app.Exec(w.ctx, `SELECT * FROM identity.team_create($1, $2)`, w.ownerSess, strings.Repeat("x", 65)); pgCode(err) != "22023" {
		t.Fatalf("team_create(65 runes) = %v, want SQLSTATE 22023", err)
	}
	if _, err := w.app.Exec(w.ctx, `SELECT * FROM identity.team_create($1, $2)`, w.ownerSess, strings.Repeat("x", 64)); err != nil {
		t.Fatalf("team_create(64 runes) = %v, want success", err)
	}
	if _, err := store.CreateTeam(w.ctx, w.ownerSess, "\u00a0Team"); err != nil {
		t.Fatalf("CreateTeam(U+00A0 prefix) = %v, want success", err)
	}
	var trimmed string
	if err := w.owner.QueryRow(w.ctx, `SELECT name FROM identity.teams WHERE name = 'Team'`).Scan(&trimmed); err != nil || trimmed != "Team" {
		t.Fatalf("U+00A0-prefixed team name = %q, err %v, want Team", trimmed, err)
	}
	if _, err := store.CreateTeam(w.ctx, w.ownerSess, "Tail \t "); err != nil {
		t.Fatalf("CreateTeam(trailing space) = %v, want success", err)
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT name FROM identity.teams WHERE name = 'Tail'`).Scan(&trimmed); err != nil || trimmed != "Tail" {
		t.Fatalf("trailing-space team name = %q, err %v, want Tail", trimmed, err)
	}
	if _, err := store.CreateTeam(w.ctx, w.ownerSess, strings.Repeat("é", 64)); err != nil {
		t.Fatalf("CreateTeam(64 two-byte runes) = %v, want success", err)
	}
	if _, err := store.CreateTeam(w.ctx, w.ownerSess, strings.Repeat("é", 65)); !errors.Is(err, domain.ErrBadTeam) {
		t.Fatalf("CreateTeam(65 two-byte runes) = %v, want ErrBadTeam", err)
	}
	if _, err := w.app.Exec(w.ctx, `SELECT * FROM identity.team_create($1, $2)`, w.ownerSess, strings.Repeat("ü", 65)); pgCode(err) != "22023" {
		t.Fatalf("team_create(65 two-byte runes) = %v, want SQLSTATE 22023", err)
	}
	if _, err := store.CreateTeam(w.ctx, w.ownerSess, "Existing"); err != nil {
		t.Fatal(err)
	}
	events = w.count(t, "audit.pending WHERE action = 'team.create'")
	if _, err := store.CreateTeam(w.ctx, w.ownerSess, "existing"); !errors.Is(err, domain.ErrTeamNameTaken) {
		t.Fatalf("CreateTeam(case-insensitive duplicate) = %v, want ErrTeamNameTaken", err)
	}
	if got := w.count(t, "audit.pending WHERE action = 'team.create'"); got != events {
		t.Fatalf("team.create events after duplicate refusal = %d, want %d", got, events)
	}
}

func TestTeamCreateEndedSessionReturns401(t *testing.T) {
	w := newDefinerWorld(t)
	adminID := w.person(t, "admin@example.com", "Admin", w.roleID(t, "admin"), true, false)
	sessions := identity.Sessions(w.app, domain.IdleTimeout, domain.MaxAge)
	userID, err := domain.ParseUserID(adminID)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := sessions.Start(w.ctx, userID, "passkey", "test")
	if err != nil {
		t.Fatal(err)
	}
	sessions.Store = endingSessions{Store: sessions.Store, owner: w.owner}
	mux := http.NewServeMux()
	identity.MountTeams(mux, identity.Teams(w.app, nil), sessions)
	rec := call(mux, http.MethodPost, "/api/teams", token, jsonBody(t, map[string]string{"name": "Raced"}))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ended-session team create = %d %s, want 401", rec.Code, rec.Body.String())
	}
	if got := w.count(t, "identity.teams WHERE name = 'Raced'"); got != 0 {
		t.Fatalf("teams after ended session = %d, want 0", got)
	}
}

// names_of answers only the enabled people and the teams asked for, with no
// other column, and Members.NamesOf gives the same maps.
func TestNamesOfReturnsOnlyAskedNames(t *testing.T) {
	w := newDefinerWorld(t)
	bob := w.person(t, "bob@example.com", "Bob", w.roleID(t, "member"), true, true)
	t1 := w.team(t, "T1")
	w.team(t, "T2")
	strangerPerson, strangerTeam := w.randomUUID(t), w.randomUUID(t)

	rows, err := w.app.Query(w.ctx, `SELECT * FROM identity.names_of($1, $2::uuid[], $3::uuid[])`,
		w.ownerSess, []string{w.ownerID, bob, strangerPerson}, []string{t1, strangerTeam})
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for _, f := range rows.FieldDescriptions() {
		columns = append(columns, f.Name)
	}
	got := map[string]string{}
	for rows.Next() {
		var kind, id, name string
		if err := rows.Scan(&kind, &id, &name); err != nil {
			t.Fatal(err)
		}
		got[kind+" "+id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(columns) != 3 || columns[0] != "kind" || columns[1] != "id" || columns[2] != "name" {
		t.Fatalf("columns = %v, want kind, id, name", columns)
	}
	want := map[string]string{"person " + w.ownerID: "Ada", "team " + t1: "T1"}
	if !maps.Equal(got, want) {
		t.Fatalf("names_of = %v, want %v", got, want)
	}

	members := identity.Members(w.app, nil, "", nil, nil)
	people, teams, err := members.NamesOf(w.ctx, w.ownerSess, []string{w.ownerID, bob, strangerPerson}, []string{t1, strangerTeam})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(people, map[string]string{w.ownerID: "Ada"}) {
		t.Fatalf("people = %v, want only Ada", people)
	}
	if !maps.Equal(teams, map[string]string{t1: "T1"}) {
		t.Fatalf("teams = %v, want only T1", teams)
	}

	// The store maps a refused actor to ErrNotSignedIn.
	if _, _, err := members.NamesOf(w.ctx, w.bad["random"], []string{w.ownerID}, nil); !errors.Is(err, identity.ErrNotSignedIn) {
		t.Fatalf("NamesOf with a random hash = %v, want ErrNotSignedIn", err)
	}
}

// pgCode is the SQLSTATE of err, or empty when err is no Postgres error.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// session is the app session of a person with a live session hash, as the
// identity routes build it from the cookie.
func (w *definerWorld) session(t *testing.T, userID string, hash []byte) app.Session {
	t.Helper()
	id, err := domain.ParseUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	return app.Session{UserID: id, TokenHash: hash, SignedIn: time.Now()}
}

// live seeds a person with a live session and returns both.
func (w *definerWorld) live(t *testing.T, email, name, roleKey string) (string, []byte) {
	t.Helper()
	id := w.person(t, email, name, w.roleID(t, roleKey), true, false)
	hash := randomHash(t)
	seedSess(t, w.owner, w.ctx, id, hash, time.Now().Add(time.Hour))
	return id, hash
}

// Every actor definer is private: no PUBLIC execute, upmo_app may execute,
// and each runs as its owner with a pinned search path.
func TestActorDefinersArePrivate(t *testing.T) {
	w := newDefinerWorld(t)
	for _, fn := range actorDefiners {
		var public, appExec, definer, pinned bool
		if err := w.owner.QueryRow(w.ctx, `
			SELECT has_function_privilege('public', $1::regprocedure, 'EXECUTE'),
			       has_function_privilege('upmo_app', $1::regprocedure, 'EXECUTE'),
			       proc.prosecdef,
			       'search_path=pg_catalog' = ANY (COALESCE(proc.proconfig, '{}'))
			FROM pg_proc AS proc
			WHERE proc.oid = $1::regprocedure`, fn).Scan(&public, &appExec, &definer, &pinned); err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		if public || !appExec || !definer || !pinned {
			t.Fatalf("%s: public=%v upmo_app=%v security definer=%v search_path pinned=%v, want false true true true", fn, public, appExec, definer, pinned)
		}
	}
}

// role_directory counts only the enabled people on each role, ordered by
// role name, and Members.Roles answers it.
func TestRoleDirectoryCounts(t *testing.T) {
	w := newDefinerWorld(t)
	if _, err := w.owner.Exec(w.ctx, `INSERT INTO identity.roles (name, permissions) VALUES ('Auditors', ARRAY['audit:view'])`); err != nil {
		t.Fatal(err)
	}
	member := w.roleID(t, "member")
	w.person(t, "m1@example.com", "M1", member, true, false)
	w.person(t, "m2@example.com", "M2", member, true, false)
	w.person(t, "m3@example.com", "M3", member, true, true)

	members := identity.Members(w.app, nil, "", nil, nil)
	roles, err := members.Roles(w.ctx, w.session(t, w.ownerID, w.ownerSess))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, role := range roles {
		got = append(got, role.Name+"="+strconv.Itoa(role.Members))
	}
	// Admin holds the unjoined New (enabled) but not the disabled Gone.
	want := []string{"Admin=1", "Auditors=0", "Member=2", "Owner=1", "Viewer=0"}
	if !slices.Equal(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	if _, err := members.Roles(w.ctx, w.session(t, w.ownerID, w.bad["random"])); !errors.Is(err, identity.ErrNotSignedIn) {
		t.Fatalf("Roles with a random hash = %v, want ErrNotSignedIn", err)
	}
}

// object_grants lists exactly the person and team grants, with names and
// levels in the order they were made, and refuses an unknown object type.
func TestObjectGrantsMatchToday(t *testing.T) {
	w := newDefinerWorld(t)
	bob := w.person(t, "bob@example.com", "Bob", w.roleID(t, "member"), true, false)
	t1 := w.team(t, "T1")
	task := w.randomUUID(t)
	for _, insert := range []struct {
		column, value, level string
	}{
		{"user_id", bob, "editor"},
		{"team_id", t1, "viewer"},
		{"grantee_role_id", w.roleID(t, "viewer"), "viewer"},
	} {
		if _, err := w.owner.Exec(w.ctx, `
			INSERT INTO identity.shares (object_type, object_id, `+insert.column+`, level, granted_by)
			VALUES ('task', $1::uuid, $2::uuid, $3, $4::uuid)`, task, insert.value, insert.level, w.ownerID); err != nil {
			t.Fatal(err)
		}
	}
	want := []app.TaskGrant{
		{SubjectType: "person", SubjectID: bob, Name: "Bob", Level: "editor"},
		{SubjectType: "team", SubjectID: t1, Name: "T1", Level: "viewer"},
	}
	members := identity.Members(w.app, nil, "", nil, nil)
	got, err := members.TaskGrants(w.ctx, w.ownerSess, task)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("TaskGrants = %v, want %v", got, want)
	}
	if chats, err := members.ChatGrants(w.ctx, w.ownerSess, task); err != nil || len(chats) != 0 {
		t.Fatalf("ChatGrants of a task id = %v %v, want none", chats, err)
	}
	if _, err := w.app.Exec(w.ctx, `SELECT * FROM identity.object_grants($1, 'note', $2::uuid)`, w.ownerSess, task); pgCode(err) != "22023" {
		t.Fatalf("object_grants note = %v, want SQLSTATE 22023", err)
	}
	if _, err := members.TaskGrants(w.ctx, w.bad["expired"], task); !errors.Is(err, identity.ErrNotSignedIn) {
		t.Fatalf("TaskGrants with an expired hash = %v, want ErrNotSignedIn", err)
	}
}

// shareRow is one stored grant as the replace test reads it back.
type shareRow struct {
	subject, level, grantedBy string
}

func (w *definerWorld) shares(t *testing.T, task string) []shareRow {
	t.Helper()
	rows, err := w.owner.Query(w.ctx, `
		SELECT COALESCE(user_id::text, team_id::text, 'role:' || grantee_role_id::text), level, granted_by::text
		FROM identity.shares
		WHERE object_type = 'task' AND object_id = $1::uuid`, task)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (shareRow, error) {
		var r shareRow
		err := row.Scan(&r.subject, &r.level, &r.grantedBy)
		return r, err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Sorted in Go: the database collation may order the ids differently.
	slices.SortFunc(out, func(a, b shareRow) int { return strings.Compare(a.subject, b.subject) })
	return out
}

// replace_object_grants runs in the caller's transaction, stores the actor
// as grantor, leaves role grants alone and refuses a bad grantee whole.
func TestReplaceObjectGrantsBindsTheActor(t *testing.T) {
	w := newDefinerWorld(t)
	bob := w.person(t, "bob@example.com", "Bob", w.roleID(t, "member"), true, false)
	cy := w.person(t, "cy@example.com", "Cy", w.roleID(t, "member"), true, false)
	dee := w.person(t, "dee@example.com", "Dee", w.roleID(t, "member"), true, false)
	ex := w.person(t, "ex@example.com", "Ex", w.roleID(t, "member"), true, true)
	t1 := w.team(t, "T1")
	viewerRole := w.roleID(t, "viewer")
	task := w.randomUUID(t)
	for _, seed := range []struct{ column, value, level string }{
		{"user_id", bob, "viewer"},
		{"user_id", dee, "viewer"},
		{"grantee_role_id", viewerRole, "viewer"},
	} {
		if _, err := w.owner.Exec(w.ctx, `
			INSERT INTO identity.shares (object_type, object_id, `+seed.column+`, level, granted_by)
			VALUES ('task', $1::uuid, $2::uuid, $3, $4::uuid)`, task, seed.value, seed.level, cy); err != nil {
			t.Fatal(err)
		}
	}
	before := w.shares(t, task)
	members := identity.Members(w.app, nil, "", nil, nil)
	grants := []identity.GrantWrite{
		{SubjectType: "person", SubjectID: bob, Level: "editor"},
		{SubjectType: "team", SubjectID: t1, Level: "viewer"},
		{SubjectType: "person", SubjectID: cy, Level: "viewer"},
	}
	replace := func(commit bool) {
		t.Helper()
		tx, err := w.app.Begin(w.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(w.ctx) }()
		if err := members.ReplaceTaskGrantsTx(w.ctx, w.ownerSess, tx, task, grants); err != nil {
			t.Fatal(err)
		}
		if commit {
			if err := tx.Commit(w.ctx); err != nil {
				t.Fatal(err)
			}
		}
	}

	replace(false)
	if got := w.shares(t, task); !slices.Equal(got, before) {
		t.Fatalf("after rollback shares = %v, want %v unchanged", got, before)
	}

	replace(true)
	want := []shareRow{
		{bob, "editor", cy},
		{cy, "viewer", w.ownerID},
		{t1, "viewer", w.ownerID},
		{"role:" + viewerRole, "viewer", cy},
	}
	slices.SortFunc(want, func(a, b shareRow) int { return strings.Compare(a.subject, b.subject) })
	after := w.shares(t, task)
	if !slices.Equal(after, want) {
		t.Fatalf("after commit shares = %v, want %v", after, want)
	}

	for name, c := range map[string]struct {
		grants []identity.GrantWrite
		code   string
	}{
		"disabled person": {[]identity.GrantWrite{{SubjectType: "person", SubjectID: bob, Level: "viewer"}, {SubjectType: "person", SubjectID: ex, Level: "viewer"}}, "P0002"},
		"unknown team":    {[]identity.GrantWrite{{SubjectType: "team", SubjectID: w.randomUUID(t), Level: "viewer"}}, "P0002"},
		"role subject":    {[]identity.GrantWrite{{SubjectType: "role", SubjectID: viewerRole, Level: "viewer"}}, "22023"},
		"bad subject id":  {[]identity.GrantWrite{{SubjectType: "person", SubjectID: "bob", Level: "viewer"}}, "22023"},
		"duplicate":       {[]identity.GrantWrite{{SubjectType: "person", SubjectID: bob, Level: "viewer"}, {SubjectType: "person", SubjectID: strings.ToUpper(bob), Level: "editor"}}, "22023"},
		"bad level":       {[]identity.GrantWrite{{SubjectType: "person", SubjectID: bob, Level: "owner"}}, "23514"},
	} {
		if err := members.ReplaceTaskGrants(w.ctx, w.ownerSess, task, c.grants); pgCode(err) != c.code {
			t.Fatalf("%s: err = %v, want SQLSTATE %s", name, err, c.code)
		}
		if got := w.shares(t, task); !slices.Equal(got, after) {
			t.Fatalf("%s: shares = %v, want %v unchanged", name, got, after)
		}
	}
	if err := members.ReplaceChatGrants(w.ctx, w.bad["disabled"], task, nil); !errors.Is(err, identity.ErrNotSignedIn) {
		t.Fatalf("ReplaceChatGrants with a disabled actor = %v, want ErrNotSignedIn", err)
	}
}

// teamEvents counts the team.member_add events waiting to be sealed.
func (w *definerWorld) teamEvents(t *testing.T) int {
	t.Helper()
	return w.count(t, "audit.pending WHERE action = 'team.member_add'")
}

// removeEvents counts the team.member_remove events waiting to be sealed.
func (w *definerWorld) removeEvents(t *testing.T) int {
	t.Helper()
	return w.count(t, "audit.pending WHERE action = 'team.member_remove'")
}

// atOnce starts every call in its own goroutine at one signal and answers
// the first error.
func atOnce(calls ...func() error) error {
	start := make(chan struct{})
	errs := make(chan error, len(calls))
	for _, call := range calls {
		go func() {
			<-start
			errs <- call()
		}()
	}
	close(start)
	var first error
	for range calls {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}

// endingSessions ends a session right after the route looked it up, so the
// definer sees a session the route still holds.
type endingSessions struct {
	app.Store
	owner *pgxpool.Pool
}

func (s endingSessions) Find(ctx context.Context, hash []byte) (app.Row, bool, error) {
	row, found, err := s.Store.Find(ctx, hash)
	if err == nil && found {
		_, err = s.owner.Exec(ctx, `DELETE FROM identity.sessions WHERE token_hash = $1`, hash)
	}
	return row, found, err
}

// add_team_member needs teams:manage, keeps today's refusals, and records
// one team.member_add event only when the person was added.
func TestAddTeamMemberDefiner(t *testing.T) {
	w := newDefinerWorld(t)
	admin, adminSess := w.live(t, "ann@example.com", "Ann", "admin")
	mo, moSess := w.live(t, "mo@example.com", "Mo", "member")
	t1 := w.team(t, "T1")
	teamID, err := domain.ParseTeamID(t1)
	if err != nil {
		t.Fatal(err)
	}
	user := func(id string) domain.UserID {
		t.Helper()
		parsed, err := domain.ParseUserID(id)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	teams := identity.Teams(w.app, nil)
	as := w.session(t, admin, adminSess)

	if err := teams.Add(w.ctx, as, teamID, user(w.ownerID)); err != nil {
		t.Fatal(err)
	}
	if got := w.teamEvents(t); got != 1 {
		t.Fatalf("team.member_add events = %d, want 1", got)
	}
	if err := teams.Add(w.ctx, as, teamID, user(w.ownerID)); err != nil {
		t.Fatal(err)
	}
	if got := w.teamEvents(t); got != 1 {
		t.Fatalf("team.member_add events after a repeat = %d, want still 1", got)
	}
	if got := w.count(t, "identity.team_members"); got != 1 {
		t.Fatalf("team_members = %d, want 1", got)
	}

	if _, err := w.app.Exec(w.ctx, `SELECT * FROM identity.add_team_member($1, $2::uuid, $3::uuid)`, moSess, t1, admin); pgCode(err) != "42501" {
		t.Fatalf("member add_team_member = %v, want SQLSTATE 42501", err)
	}
	if err := teams.Add(w.ctx, w.session(t, mo, moSess), teamID, user(admin)); !errors.Is(err, domain.ErrTeamsOnly) {
		t.Fatalf("member Add = %v, want ErrTeamsOnly", err)
	}

	var unjoined, disabled string
	if err := w.owner.QueryRow(w.ctx, `SELECT id::text FROM identity.users WHERE name = 'New'`).Scan(&unjoined); err != nil {
		t.Fatal(err)
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT id::text FROM identity.users WHERE name = 'Gone'`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	unknownTeam, err := domain.ParseTeamID(w.randomUUID(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		team domain.TeamID
		user string
		want error
	}{
		"unjoined":     {teamID, unjoined, domain.ErrNotJoined},
		"unknown team": {unknownTeam, admin, domain.ErrNoSuchTeam},
		"disabled":     {teamID, disabled, domain.ErrNoSuchMember},
		"unknown user": {teamID, w.randomUUID(t), domain.ErrNoSuchMember},
	} {
		if err := teams.Add(w.ctx, as, c.team, user(c.user)); !errors.Is(err, c.want) {
			t.Fatalf("%s: Add = %v, want %v", name, err, c.want)
		}
	}
	if got := w.count(t, "identity.team_members"); got != 1 {
		t.Fatalf("team_members after refusals = %d, want 1", got)
	}

	// A session that ends between the route's lookup and the definer call
	// answers 401, not 500.
	sessions := identity.Sessions(w.app, domain.IdleTimeout, domain.MaxAge)
	sessions.Store = endingSessions{Store: sessions.Store, owner: w.owner}
	text, _, err := sessions.Start(w.ctx, user(admin), "passkey", "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	identity.MountTeams(mux, teams, sessions)
	rec := call(mux, http.MethodPost, "/api/teams/"+t1+"/members", text, jsonBody(t, map[string]string{"user_id": admin}))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ended session add = %d %s, want 401", rec.Code, rec.Body.String())
	}
	if got := w.count(t, "identity.team_members"); got != 1 {
		t.Fatalf("team_members after the ended session = %d, want 1", got)
	}
}

// remove_team_member needs a live joined actor with teams:manage, keeps the
// refusals add_team_member answers, and records one team.member_remove event
// only when the person left the team. Someone who was not on the team answers
// false with no event and no error. An unknown team is P0002, which the store
// maps to domain.ErrNoSuchTeam. Two managers removing the same person at once
// get one true, one false and one event. A session that ends after the
// route's lookup answers 401 and removes nobody.
func TestRemoveTeamMemberDefiner(t *testing.T) {
	w := newDefinerWorld(t)
	ann, annSess := w.live(t, "ann@example.com", "Ann", "admin")
	_, benSess := w.live(t, "ben@example.com", "Ben", "admin")
	bob, _ := w.live(t, "bob@example.com", "Bob", "member")
	_, moSess := w.live(t, "mo@example.com", "Mo", "member")
	t1 := w.team(t, "T1")
	teamID, err := domain.ParseTeamID(t1)
	if err != nil {
		t.Fatal(err)
	}
	user := func(id string) domain.UserID {
		t.Helper()
		parsed, err := domain.ParseUserID(id)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	bobID := user(bob)
	teams := identity.Teams(w.app, nil)
	as := w.session(t, ann, annSess)
	const remove = `SELECT * FROM identity.remove_team_member($1, $2::uuid, $3::uuid)`
	const add = `SELECT * FROM identity.add_team_member($1, $2::uuid, $3::uuid)`

	if err := teams.Add(w.ctx, as, teamID, bobID); err != nil {
		t.Fatal(err)
	}
	if err := teams.Remove(w.ctx, as, teamID, bobID); err != nil {
		t.Fatal(err)
	}
	if got := w.removeEvents(t); got != 1 {
		t.Fatalf("team.member_remove events = %d, want 1", got)
	}
	if got := w.count(t, "identity.team_members"); got != 0 {
		t.Fatalf("team_members = %d, want 0", got)
	}
	if err := teams.Remove(w.ctx, as, teamID, bobID); err != nil {
		t.Fatalf("removing someone who is not on the team: %v", err)
	}
	if got := w.removeEvents(t); got != 1 {
		t.Fatalf("team.member_remove events after a repeat = %d, want still 1", got)
	}

	// The definer itself answers true when it deleted a row and false when
	// the person was not on the team.
	if _, err := w.app.Exec(w.ctx, add, annSess, t1, bob); err != nil {
		t.Fatal(err)
	}
	var removed bool
	if err := w.app.QueryRow(w.ctx, remove, annSess, t1, bob).Scan(&removed); err != nil || !removed {
		t.Fatalf("remove_team_member for a member = %t %v, want true", removed, err)
	}
	if err := w.app.QueryRow(w.ctx, remove, annSess, t1, bob).Scan(&removed); err != nil || removed {
		t.Fatalf("remove_team_member for someone not on the team = %t %v, want false", removed, err)
	}

	unknownTeam, err := domain.ParseTeamID(w.randomUUID(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.app.Exec(w.ctx, remove, annSess, unknownTeam, bob); pgCode(err) != "P0002" {
		t.Fatalf("remove_team_member on an unknown team = %v, want SQLSTATE P0002", err)
	}
	if err := teams.Store.RemoveTeamMember(w.ctx, annSess, unknownTeam, bobID); !errors.Is(err, domain.ErrNoSuchTeam) {
		t.Fatalf("RemoveTeamMember on an unknown team = %v, want ErrNoSuchTeam", err)
	}

	// Every refusal answers 42501, maps as add_team_member's refusals map, and
	// leaves the person on the team.
	if _, err := w.app.Exec(w.ctx, add, annSess, t1, bob); err != nil {
		t.Fatal(err)
	}
	for name, hash := range w.bad {
		if _, err := w.app.Exec(w.ctx, remove, hash, t1, bob); pgCode(err) != "42501" {
			t.Fatalf("%s: remove_team_member = %v, want SQLSTATE 42501", name, err)
		}
		if err := teams.Store.RemoveTeamMember(w.ctx, hash, teamID, bobID); !errors.Is(err, app.ErrNotSignedIn) {
			t.Fatalf("%s: RemoveTeamMember = %v, want ErrNotSignedIn", name, err)
		}
		if got := w.count(t, "identity.team_members"); got != 1 {
			t.Fatalf("%s: team_members = %d, want 1", name, got)
		}
	}
	if _, err := w.app.Exec(w.ctx, remove, moSess, t1, bob); pgCode(err) != "42501" {
		t.Fatalf("member remove_team_member = %v, want SQLSTATE 42501", err)
	}
	if err := teams.Store.RemoveTeamMember(w.ctx, moSess, teamID, bobID); !errors.Is(err, domain.ErrTeamsOnly) {
		t.Fatalf("member RemoveTeamMember = %v, want ErrTeamsOnly", err)
	}
	if got := w.count(t, "identity.team_members"); got != 1 {
		t.Fatalf("team_members after the refusals = %d, want 1", got)
	}

	// Two managers removing the same person at once, each on their own
	// database session: one delete wins and the other answers false.
	if _, err := w.app.Exec(w.ctx, add, annSess, t1, bob); err != nil {
		t.Fatal(err)
	}
	answers := make(chan bool, 2)
	removeBy := func(hash []byte) func() error {
		return func() error {
			var removed bool
			if err := w.app.QueryRow(w.ctx, remove, hash, t1, bob).Scan(&removed); err != nil {
				return err
			}
			answers <- removed
			return nil
		}
	}
	if err := atOnce(removeBy(annSess), removeBy(benSess)); err != nil {
		t.Fatal(err)
	}
	trues := 0
	for range 2 {
		if <-answers {
			trues++
		}
	}
	if trues != 1 {
		t.Fatalf("two removes at once answered true %d times, want 1", trues)
	}

	// Through the store, the same race records exactly one event.
	if err := teams.Add(w.ctx, as, teamID, bobID); err != nil {
		t.Fatal(err)
	}
	before := w.removeEvents(t)
	store := teams.Store
	if err := atOnce(
		func() error { return store.RemoveTeamMember(w.ctx, annSess, teamID, bobID) },
		func() error { return store.RemoveTeamMember(w.ctx, benSess, teamID, bobID) },
	); err != nil {
		t.Fatal(err)
	}
	if got := w.removeEvents(t) - before; got != 1 {
		t.Fatalf("two removes at once recorded %d team.member_remove events, want 1", got)
	}
	if got := w.count(t, "identity.team_members"); got != 0 {
		t.Fatalf("team_members after two removes at once = %d, want 0", got)
	}

	// A session that ends between the route's lookup and the definer call
	// answers 401, not 500, and the person stays on the team.
	if err := teams.Add(w.ctx, as, teamID, bobID); err != nil {
		t.Fatal(err)
	}
	sessions := identity.Sessions(w.app, domain.IdleTimeout, domain.MaxAge)
	sessions.Store = endingSessions{Store: sessions.Store, owner: w.owner}
	text, _, err := sessions.Start(w.ctx, user(ann), "passkey", "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	identity.MountTeams(mux, teams, sessions)
	rec := call(mux, http.MethodDelete, "/api/teams/"+t1+"/members/"+bob, text, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ended session remove = %d %s, want 401", rec.Code, rec.Body.String())
	}
	if got := w.count(t, "identity.team_members"); got != 1 {
		t.Fatalf("team_members after the ended session = %d, want 1", got)
	}
}

// statementLog records the SQL text of every query a pool sends.
type statementLog struct {
	mu   sync.Mutex
	sqls []string
}

func (l *statementLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sqls = append(l.sqls, strings.TrimSpace(data.SQL))
	return ctx
}

func (l *statementLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (l *statementLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.sqls
	l.sqls = nil
	return out
}

// The store methods on these paths send only the definer call, apart from
// the transaction and the audit insert.
func TestStoreSendsOnlyDefinerCalls(t *testing.T) {
	w := newDefinerWorld(t)
	bob, _ := w.live(t, "bob@example.com", "Bob", "member")
	cy := w.person(t, "cy@example.com", "Cy", w.roleID(t, "member"), true, false)
	t1 := w.team(t, "T1")
	task := w.randomUUID(t)
	// Cy starts on the team, so the remove deletes a row whatever the order
	// of the calls below.
	if _, err := w.owner.Exec(w.ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2)`, t1, cy); err != nil {
		t.Fatal(err)
	}

	log := &statementLog{}
	cfg, err := pgxpool.ParseConfig(pgtest.AppLoginURL(t, w.owner))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = log
	pool, err := pgxpool.NewWithConfig(w.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	members := identity.Members(pool, nil, "", nil, nil)
	teams := identity.Teams(pool, nil)
	teamID, err := domain.ParseTeamID(t1)
	if err != nil {
		t.Fatal(err)
	}
	bobID, err := domain.ParseUserID(bob)
	if err != nil {
		t.Fatal(err)
	}
	cyID, err := domain.ParseUserID(cy)
	if err != nil {
		t.Fatal(err)
	}
	grants := []identity.GrantWrite{{SubjectType: "person", SubjectID: bob, Level: "viewer"}}

	// fn is the one definer each call may send.
	calls := map[string]struct {
		run func() error
		fn  string
	}{
		"NamesOf": {func() error {
			_, _, err := members.Store.NamesOf(w.ctx, w.ownerSess, []string{bob}, []string{t1})
			return err
		}, "names_of"},
		"ListRoles": {func() error {
			_, err := members.Store.ListRoles(w.ctx, w.ownerSess)
			return err
		}, "role_directory"},
		"TaskGrants": {func() error {
			_, err := members.Store.TaskGrants(w.ctx, w.ownerSess, task)
			return err
		}, "object_grants"},
		"ChatGrants": {func() error {
			_, err := members.Store.ChatGrants(w.ctx, w.ownerSess, task)
			return err
		}, "object_grants"},
		"ReplaceTaskGrantsTx": {func() error {
			tx, err := pool.Begin(w.ctx)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback(w.ctx) }()
			if err := members.Store.ReplaceTaskGrantsTx(w.ctx, w.ownerSess, tx, task, grants); err != nil {
				return err
			}
			return tx.Commit(w.ctx)
		}, "replace_object_grants"},
		"ReplaceChatGrants": {func() error {
			return members.Store.ReplaceChatGrants(w.ctx, w.ownerSess, task, grants)
		}, "replace_object_grants"},
		"AddTeamMember": {func() error {
			return teams.Store.AddTeamMember(w.ctx, w.ownerSess, teamID, bobID)
		}, "add_team_member"},
		"RemoveTeamMember": {func() error {
			return teams.Store.RemoveTeamMember(w.ctx, w.ownerSess, teamID, cyID)
		}, "remove_team_member"},
	}
	for name, call := range calls {
		log.take()
		if err := call.run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		sqls := log.take()
		definer := 0
		for _, sql := range sqls {
			switch {
			case strings.EqualFold(sql, "begin"), strings.EqualFold(sql, "commit"):
				continue
			case (name == "AddTeamMember" || name == "RemoveTeamMember") && strings.HasPrefix(sql, "INSERT INTO audit.pending"):
				continue
			}
			if !strings.HasPrefix(sql, "SELECT * FROM identity."+call.fn+"(") {
				t.Fatalf("%s sent %q, want only identity.%s", name, sql, call.fn)
			}
			definer++
		}
		if definer != 1 {
			t.Fatalf("%s sent %d identity.%s calls in %q, want 1", name, definer, call.fn, sqls)
		}
	}
	if got := w.teamEvents(t); got != 1 {
		t.Fatalf("team.member_add events = %d, want 1", got)
	}
	if got := w.removeEvents(t); got != 1 {
		t.Fatalf("team.member_remove events = %d, want 1", got)
	}
}
