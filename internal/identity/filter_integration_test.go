//go:build integration

package identity_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestListFilterAgreesWithCan(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			owner, ctx := upOwner(t)
			if _, err := owner.Exec(ctx, `
				CREATE TABLE work_objects (
					id uuid PRIMARY KEY DEFAULT uuidv7(),
					object_type text NOT NULL,
					owner_user_id uuid NOT NULL,
					visibility text NOT NULL,
					visibility_team_id uuid
				)`); err != nil {
				t.Fatal(err)
			}
			world := buildWorld(t, owner, ctx, seed)
			secureWork(t, owner, ctx)
			app := pgtest.App(t, owner)
			for _, subject := range world.subjects() {
				for _, objectType := range domain.ObjectTypes() {
					agree(t, owner, ctx, world, subject, objectType)
					rlsAgree(t, app, ctx, world, subject, objectType)
				}
			}
		})
	}
}

type held struct {
	subject domain.Subject
	label   string
}

type stored struct {
	id     string
	object domain.Object
}

type world struct {
	people  []held
	objects []stored
}

func (w world) subjects() []held {
	out := append([]held{}, w.people...)
	base := w.people[1].subject
	viewOnly := base
	viewOnly.Kind = domain.KindRun
	viewOnly.Policy = []domain.Permission{domain.ObjectPermission(domain.TypeTask, domain.ActionView)}
	out = append(out, held{viewOnly, "run view"})
	broad := base
	broad.Kind = domain.KindRun
	broad.Policy = []domain.Permission{
		domain.ObjectPermission(domain.TypeTask, domain.ActionView),
		domain.ObjectPermission(domain.TypeTask, domain.ActionDelete),
		domain.PermMembersManage,
	}
	out = append(out, held{broad, "run broad"})
	empty := base
	empty.Kind = domain.KindRun
	out = append(out, held{empty, "run empty"})
	agent := base
	agent.Kind = domain.KindAgent
	out = append(out, held{agent, "agent"})
	token := base
	token.Kind = domain.KindToken
	token.Policy = []domain.Permission{domain.ObjectPermission(domain.TypeTask, domain.ActionView)}
	out = append(out, held{token, "token view"})
	return out
}

func buildWorld(t *testing.T, pool *pgxpool.Pool, ctx context.Context, seed uint64) world {
	t.Helper()
	owner := seedOwner(t, pool, ctx, "owner@example.com")
	rng := rand.New(rand.NewPCG(seed, seed+1))

	type personSpec struct {
		email, roleKey   string
		joined, disabled bool
		custom           []string
	}
	extra := []personSpec{
		{"admin@example.com", "admin", true, false, nil},
		{"custom@example.com", "", true, false, []string{"task:view", "task:edit"}},
		{"viewer@example.com", "viewer", true, false, nil},
		{"disabled@example.com", "member", true, true, nil},
		{"invited@example.com", "member", false, false, nil},
		{"member@example.com", "member", true, false, nil},
		{"other@example.com", "member", true, false, nil},
	}
	var people []held
	people = append(people, hold(t, pool, ctx, "owner", owner, "owner", true, false, nil, nil))
	for i, spec := range extra {
		user := insertUser(t, pool, ctx, spec.email)
		var role string
		if spec.custom != nil {
			if err := pool.QueryRow(ctx, `INSERT INTO identity.roles (name, permissions) VALUES ($1, $2) RETURNING id`, fmt.Sprintf("Custom %d", i), spec.custom).Scan(&role); err != nil {
				t.Fatal(err)
			}
		}
		people = append(people, hold(t, pool, ctx, fmt.Sprintf("p%d", i), user, spec.roleKey, spec.joined, spec.disabled, spec.custom, &role))
	}

	teams := makeTeams(t, pool, ctx)
	joinTeam(t, pool, ctx, teams[0], owner)
	joinTeam(t, pool, ctx, teams[0], people[1].subject.UserID.String())
	joinTeam(t, pool, ctx, teams[0], people[2].subject.UserID.String())
	for _, person := range people {
		if person.subject.Joined && !person.subject.Disabled && rng.IntN(2) == 0 {
			joinTeam(t, pool, ctx, teams[rng.IntN(len(teams))], person.subject.UserID.String())
		}
	}
	reloadTeams(t, pool, ctx, people)

	objects := []stored{
		forceObject(t, pool, ctx, domain.TypeTask, owner, "private", ""),
		forceObject(t, pool, ctx, domain.TypeTask, owner, "team", teams[0]),
		forceObject(t, pool, ctx, domain.TypeChat, owner, "everyone", ""),
		forceObject(t, pool, ctx, domain.TypeTask, owner, "private", ""),
	}
	grant(t, pool, ctx, &objects[0], "task", people[1].subject.UserID.String(), "", "", "viewer", owner)
	grant(t, pool, ctx, &objects[3], "task", "", "", people[2].subject.RoleID.String(), "viewer", owner)

	types := domain.ObjectTypes()
	visibilities := []string{"private", "team", "everyone"}
	for i := 0; i < 56; i++ {
		objectType := types[rng.IntN(len(types))]
		visibility := visibilities[rng.IntN(len(visibilities))]
		team := ""
		if visibility == "team" {
			team = teams[rng.IntN(len(teams))]
		}
		row := forceObject(t, pool, ctx, objectType, owner, visibility, team)
		if rng.IntN(3) == 0 {
			switch rng.IntN(3) {
			case 0:
				grant(t, pool, ctx, &row, string(objectType), people[rng.IntN(len(people))].subject.UserID.String(), "", "", "editor", owner)
			case 1:
				grant(t, pool, ctx, &row, string(objectType), "", teams[rng.IntN(len(teams))], "", "viewer", owner)
			default:
				grant(t, pool, ctx, &row, string(objectType), "", "", people[rng.IntN(len(people))].subject.RoleID.String(), "viewer", owner)
			}
		}
		objects = append(objects, row)
	}
	return world{people: people, objects: objects}
}

func agree(t *testing.T, pool *pgxpool.Pool, ctx context.Context, w world, who held, objectType domain.ObjectType) {
	t.Helper()
	shared, _ := loadShared(t, pool, ctx, who, objectType)
	clause, args, err := domain.ListFilter("obj", who.subject, objectType, shared)
	if err != nil {
		t.Fatal(err)
	}
	var query string
	if clause == "FALSE" {
		query = `SELECT id::text FROM work_objects AS obj WHERE object_type = $1 AND FALSE`
		args = []any{string(objectType)}
	} else {
		query = `SELECT id::text FROM work_objects AS obj WHERE object_type = $4 AND ` + clause
		args = append(args, string(objectType))
	}
	gotRows, err := pool.Query(ctx, query, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", who.label, objectType, err, query)
	}
	defer gotRows.Close()
	var got []string
	for gotRows.Next() {
		var id string
		if err := gotRows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := gotRows.Err(); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, row := range w.objects {
		if row.object.Type != objectType {
			continue
		}
		if domain.Can(who.subject, row.object, domain.ActionView).Allow {
			want = append(want, row.id)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s %s\ngot  %v\nwant %v", who.label, objectType, got, want)
	}
}

func hold(t *testing.T, pool *pgxpool.Pool, ctx context.Context, label, user, roleKey string, joined, disabled bool, custom []string, customID *string) held {
	t.Helper()
	var role string
	if customID != nil && *customID != "" {
		role = *customID
	} else {
		if err := pool.QueryRow(ctx, `SELECT id FROM identity.roles WHERE key = $1`, roleKey).Scan(&role); err != nil {
			t.Fatal(err)
		}
	}
	column := "joined_at"
	if !joined {
		column = "invited_at"
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`UPDATE identity.users SET role_id = $2, %s = now() WHERE id = $1`, column), user, role); err != nil {
		t.Fatal(err)
	}
	if disabled {
		if _, err := pool.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, user); err != nil {
			t.Fatal(err)
		}
	}
	permissions := customPermissions(t, roleKey, custom)
	subject := domain.Subject{
		Kind:        domain.KindUser,
		UserID:      mustUser(t, user),
		RoleID:      mustRole(t, role),
		Joined:      joined,
		Disabled:    disabled,
		Permissions: permissions,
	}
	return held{subject, label}
}

func customPermissions(t *testing.T, roleKey string, custom []string) []domain.Permission {
	t.Helper()
	if custom != nil {
		out := make([]domain.Permission, len(custom))
		for i, name := range custom {
			permission, err := domain.ParsePermission(name)
			if err != nil {
				t.Fatal(err)
			}
			out[i] = permission
		}
		return out
	}
	permissions, err := domain.SystemPermissions(domain.Role(roleKey))
	if err != nil {
		t.Fatal(err)
	}
	return permissions
}

func makeTeams(t *testing.T, pool *pgxpool.Pool, ctx context.Context) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= 3; i++ {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id`, fmt.Sprintf("Team %d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func joinTeam(t *testing.T, pool *pgxpool.Pool, ctx context.Context, team, user string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, team, user); err != nil {
		t.Fatal(err)
	}
}

func reloadTeams(t *testing.T, pool *pgxpool.Pool, ctx context.Context, people []held) {
	t.Helper()
	for i := range people {
		rows, err := pool.Query(ctx, `SELECT team_id::text FROM identity.team_members WHERE user_id = $1`, people[i].subject.UserID.String())
		if err != nil {
			t.Fatal(err)
		}
		var teams []domain.TeamID
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			teams = append(teams, mustTeam(t, id))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		people[i].subject.Teams = teams
	}
}

func forceObject(t *testing.T, pool *pgxpool.Pool, ctx context.Context, objectType domain.ObjectType, owner, visibility, team string) stored {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO work_objects (object_type, owner_user_id, visibility, visibility_team_id)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid) RETURNING id::text`,
		string(objectType), owner, visibility, team).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	object := domain.Object{
		Type:       objectType,
		ID:         id,
		OwnerID:    mustUser(t, owner),
		Visibility: domain.Visibility(visibility),
	}
	if team != "" {
		object.VisibilityTeam = mustTeam(t, team)
	}
	return stored{id, object}
}

func grant(t *testing.T, pool *pgxpool.Pool, ctx context.Context, row *stored, objectType, user, team, role, level, by string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, team_id, grantee_role_id, level, granted_by)
		VALUES ($1, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7)`,
		objectType, row.id, user, team, role, level, by)
	if err != nil {
		t.Fatal(err)
	}
	share := domain.Grant{Level: domain.Level(level)}
	if user != "" {
		share.UserID = mustUser(t, user)
	}
	if team != "" {
		share.TeamID = mustTeam(t, team)
	}
	if role != "" {
		share.RoleID = mustRole(t, role)
	}
	row.object.Shares = append(row.object.Shares, share)
}

func insertUser(t *testing.T, pool *pgxpool.Pool, ctx context.Context, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, sha256(convert_to($1, 'UTF8')), 'k1', 'Person', (SELECT id FROM identity.roles WHERE key = 'member'), now()) RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func mustUser(t *testing.T, id string) domain.UserID {
	t.Helper()
	parsed, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func mustTeam(t *testing.T, id string) domain.TeamID {
	t.Helper()
	parsed, err := domain.ParseTeamID(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func mustRole(t *testing.T, id string) domain.RoleID {
	t.Helper()
	parsed, err := domain.ParseRoleID(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func loadShared(t *testing.T, pool *pgxpool.Pool, ctx context.Context, who held, objectType domain.ObjectType) (view, edit []string) {
	t.Helper()
	teams := make([]string, len(who.subject.Teams))
	for i, team := range who.subject.Teams {
		teams[i] = team.String()
	}
	rows, err := pool.Query(ctx, domain.SharedObjectsSQL(), string(objectType), who.subject.UserID.String(), teams, who.subject.RoleID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, level string
		if err := rows.Scan(&id, &level); err != nil {
			t.Fatal(err)
		}
		view = append(view, id)
		if level == string(domain.LevelEditor) {
			edit = append(edit, id)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return view, edit
}

func secureWork(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE work_secure (
			id uuid PRIMARY KEY,
			object_type text NOT NULL,
			owner_user_id uuid NOT NULL,
			visibility text NOT NULL,
			visibility_team_id uuid
		);
		INSERT INTO work_secure SELECT * FROM work_objects;
		SELECT identity.protect('work_secure'::regclass);
	`); err != nil {
		t.Fatal(err)
	}
}

func rlsAgree(t *testing.T, pool *pgxpool.Pool, ctx context.Context, w world, who held, objectType domain.ObjectType) {
	t.Helper()
	view, edit := loadShared(t, pool, ctx, who, objectType)
	gotView := rlsReturning(t, pool, ctx, who, objectType, view, edit, `SELECT id::text FROM work_secure WHERE object_type = $1`)
	gotEdit := rlsReturning(t, pool, ctx, who, objectType, view, edit, `UPDATE work_secure SET visibility = visibility WHERE object_type = $1 RETURNING id::text`)
	gotDelete := rlsReturning(t, pool, ctx, who, objectType, view, edit, `DELETE FROM work_secure WHERE object_type = $1 RETURNING id::text`)
	wantView, wantEdit, wantDelete := wantIDs(w, who, objectType)
	if !slices.Equal(gotView, wantView) {
		t.Fatalf("%s %s view\ngot  %v\nwant %v", who.label, objectType, gotView, wantView)
	}
	if !slices.Equal(gotEdit, wantEdit) {
		t.Fatalf("%s %s edit\ngot  %v\nwant %v", who.label, objectType, gotEdit, wantEdit)
	}
	if !slices.Equal(gotDelete, wantDelete) {
		t.Fatalf("%s %s delete\ngot  %v\nwant %v", who.label, objectType, gotDelete, wantDelete)
	}
	rlsCreate(t, pool, ctx, w, who, objectType, view, edit)
}

func wantIDs(w world, who held, objectType domain.ObjectType) (view, edit, del []string) {
	for _, row := range w.objects {
		if row.object.Type != objectType {
			continue
		}
		if domain.Can(who.subject, row.object, domain.ActionView).Allow {
			view = append(view, row.id)
		}
		if domain.Can(who.subject, row.object, domain.ActionEdit).Allow {
			edit = append(edit, row.id)
		}
		if domain.Can(who.subject, row.object, domain.ActionDelete).Allow {
			del = append(del, row.id)
		}
	}
	slices.Sort(view)
	slices.Sort(edit)
	slices.Sort(del)
	return view, edit, del
}

func rlsReturning(t *testing.T, pool *pgxpool.Pool, ctx context.Context, who held, objectType domain.ObjectType, view, edit []string, query string) []string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	applyAccess(t, tx, ctx, who, objectType, view, edit)
	rows, err := tx.Query(ctx, query, string(objectType))
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", who.label, objectType, err, query)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	slices.Sort(ids)
	return ids
}

func rlsCreate(t *testing.T, pool *pgxpool.Pool, ctx context.Context, w world, who held, objectType domain.ObjectType, view, edit []string) {
	t.Helper()
	allowed := domain.Permitted(who.subject, domain.ObjectPermission(objectType, domain.ActionCreate)).Allow
	own := insertSecure(t, pool, ctx, who, objectType, view, edit, who.subject.UserID.String())
	if own != allowed {
		t.Fatalf("%s %s create own: allowed=%v want %v", who.label, objectType, own, allowed)
	}
	other := w.people[0].subject.UserID.String()
	if other == who.subject.UserID.String() && len(w.people) > 1 {
		other = w.people[1].subject.UserID.String()
	}
	if insertSecure(t, pool, ctx, who, objectType, view, edit, other) {
		t.Fatalf("%s %s create for another person was allowed", who.label, objectType)
	}
}

func insertSecure(t *testing.T, pool *pgxpool.Pool, ctx context.Context, who held, objectType domain.ObjectType, view, edit []string, owner string) bool {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	applyAccess(t, tx, ctx, who, objectType, view, edit)
	_, err = tx.Exec(ctx, `
		INSERT INTO work_secure (id, object_type, owner_user_id, visibility)
		VALUES (uuidv7(), $1, $2, 'private')`, string(objectType), owner)
	return err == nil
}

func applyAccess(t *testing.T, tx pgx.Tx, ctx context.Context, who held, objectType domain.ObjectType, view, edit []string) {
	t.Helper()
	sql, args := domain.AccessSettings(who.subject, objectType, view, edit)
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestRowSecurityFailsClosedWithoutSettings(t *testing.T) {
	owner, ctx := upOwner(t)
	if _, err := owner.Exec(ctx, `
		CREATE TABLE work_objects (
			id uuid PRIMARY KEY DEFAULT uuidv7(),
			object_type text NOT NULL,
			owner_user_id uuid NOT NULL,
			visibility text NOT NULL,
			visibility_team_id uuid
		)`); err != nil {
		t.Fatal(err)
	}
	world := buildWorld(t, owner, ctx, 1)
	secureWork(t, owner, ctx)
	app := pgtest.App(t, owner)
	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM work_secure`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("no settings returned %d rows", n)
	}
	who := world.people[0]
	view, edit := loadShared(t, app, ctx, who, domain.TypeTask)
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	applyAccess(t, tx, ctx, who, domain.TypeTask, view, edit)
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM work_secure WHERE object_type = 'task'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("owner saw no tasks with settings")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.QueryRow(ctx, `SELECT count(*) FROM work_secure`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("settings leaked into the next transaction: %d", n)
	}
}

func TestAppRoleCannotChangeSchema(t *testing.T) {
	owner, ctx := upOwner(t)
	app := pgtest.App(t, owner)
	if _, err := app.Exec(ctx, `CREATE TABLE work_nope (id uuid)`); err == nil {
		t.Fatal("upmo_app created a table")
	}
	if _, err := app.Exec(ctx, `DROP TABLE identity.users`); err == nil {
		t.Fatal("upmo_app dropped users")
	}
	if _, err := app.Exec(ctx, `SELECT * FROM identity.goose_db_version`); err == nil {
		t.Fatal("upmo_app read the version table")
	}
	var bypass bool
	if err := app.QueryRow(ctx, `SELECT rolbypassrls FROM pg_roles WHERE rolname = 'upmo_app'`).Scan(&bypass); err != nil {
		t.Fatal(err)
	}
	if bypass {
		t.Fatal("upmo_app has BYPASSRLS")
	}
	var mayProtect bool
	if err := app.QueryRow(ctx, `SELECT has_function_privilege('upmo_app', 'identity.protect(regclass)', 'EXECUTE')`).Scan(&mayProtect); err != nil {
		t.Fatal(err)
	}
	if mayProtect {
		t.Fatal("upmo_app can run identity.protect")
	}
}

func TestGuardWorkTablesHaveRowSecurity(t *testing.T) {
	pool, ctx := up(t)
	rows, err := pool.Query(ctx, `
		SELECT n.nspname || '.' || c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'owner_user_id' AND NOT a.attisdropped
		WHERE c.relkind = 'r' AND n.nspname NOT IN ('identity', 'pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		gaps, err := protectionGaps(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if len(gaps) > 0 {
			t.Fatalf("%s: %s", name, strings.Join(gaps, "; "))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func protectionGaps(ctx context.Context, pool *pgxpool.Pool, name string) ([]string, error) {
	var enabled, forced bool
	if err := pool.QueryRow(ctx, `
		SELECT c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c WHERE c.oid = $1::regclass`, name).Scan(&enabled, &forced); err != nil {
		return nil, err
	}
	var gaps []string
	if !enabled {
		gaps = append(gaps, "row security disabled")
	}
	if !forced {
		gaps = append(gaps, "row security not forced")
	}
	for _, priv := range []string{"SELECT", "INSERT", "DELETE"} {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT has_table_privilege('upmo_app', $1::regclass, $2)`, name, priv).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			gaps = append(gaps, "missing "+priv)
		}
	}
	var anyUpdate bool
	if err := pool.QueryRow(ctx, `SELECT has_any_column_privilege('upmo_app', $1::regclass, 'UPDATE')`, name).Scan(&anyUpdate); err != nil {
		return nil, err
	}
	if !anyUpdate {
		gaps = append(gaps, "missing UPDATE")
	}
	var ownerUpdate bool
	if err := pool.QueryRow(ctx, `SELECT has_column_privilege('upmo_app', $1::regclass, 'owner_user_id', 'UPDATE')`, name).Scan(&ownerUpdate); err != nil {
		return nil, err
	}
	if ownerUpdate {
		gaps = append(gaps, "owner_user_id is updatable")
	}
	var nsp string
	if err := pool.QueryRow(ctx, `
		SELECT n.nspname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.oid = $1::regclass`, name).Scan(&nsp); err != nil {
		return nil, err
	}
	var usage bool
	if err := pool.QueryRow(ctx, `SELECT has_schema_privilege('upmo_app', $1, 'USAGE')`, nsp).Scan(&usage); err != nil {
		return nil, err
	}
	if !usage {
		gaps = append(gaps, "schema usage missing")
	}
	pol, err := pool.Query(ctx, `
		SELECT polcmd::text,
			COALESCE(pg_get_expr(polqual, polrelid), ''),
			COALESCE(pg_get_expr(polwithcheck, polrelid), '')
		FROM pg_policy WHERE polrelid = $1::regclass`, name)
	if err != nil {
		return nil, err
	}
	defer pol.Close()
	cmds := map[string]int{}
	for pol.Next() {
		var cmd, qual, check string
		if err := pol.Scan(&cmd, &qual, &check); err != nil {
			return nil, err
		}
		switch cmd {
		case "r":
			if !strings.Contains(qual, "identity.may") {
				gaps = append(gaps, "select policy")
			}
		case "a":
			if !strings.Contains(check, "identity.may") {
				gaps = append(gaps, "insert policy")
			}
		case "w":
			if !strings.Contains(qual, "identity.may") || !strings.Contains(check, "identity.may") {
				gaps = append(gaps, "update policy")
			}
		case "d":
			if !strings.Contains(qual, "identity.may") {
				gaps = append(gaps, "delete policy")
			}
		default:
			gaps = append(gaps, "unexpected command "+cmd)
		}
		cmds[cmd]++
	}
	if err := pol.Err(); err != nil {
		return nil, err
	}
	var total int
	for _, n := range cmds {
		total += n
	}
	if total != 4 {
		gaps = append(gaps, fmt.Sprintf("%d policies, want 4", total))
	}
	for _, want := range []string{"r", "a", "w", "d"} {
		if cmds[want] != 1 {
			gaps = append(gaps, "policy "+want)
		}
	}
	return gaps, nil
}

func TestRowSecurityMutationDetectsDroppedClauses(t *testing.T) {
	owner, ctx := upOwner(t)
	if _, err := owner.Exec(ctx, `
		CREATE TABLE work_objects (
			id uuid PRIMARY KEY DEFAULT uuidv7(),
			object_type text NOT NULL,
			owner_user_id uuid NOT NULL,
			visibility text NOT NULL,
			visibility_team_id uuid
		)`); err != nil {
		t.Fatal(err)
	}
	world := buildWorld(t, owner, ctx, 1)
	secureWork(t, owner, ctx)
	app := pgtest.App(t, owner)
	var original string
	if err := owner.QueryRow(ctx, `SELECT pg_get_functiondef('identity.may(text,text,uuid,text,uuid,uuid)'::regprocedure)`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(ctx, original); err != nil {
			t.Errorf("restore identity.may: %v", err)
		}
	})
	cases := []struct {
		name, old, new, query string
		want                  func(view, edit, del []string) []string
	}{
		{"team", "visibility = 'team'", "visibility = 'never-team'", `SELECT id::text FROM work_secure WHERE object_type = $1`, func(view, _, _ []string) []string { return view }},
		{"editor", "action IN ('edit', 'delete')", "action IN ('never-edit', 'never-delete')", `UPDATE work_secure SET visibility = visibility WHERE object_type = $1 RETURNING id::text`, func(_, edit, _ []string) []string { return edit }},
		{"permission", "object_type || ':' || action", "object_type || ':never-' || action", `SELECT id::text FROM work_secure WHERE object_type = $1`, func(view, _, _ []string) []string { return view }},
	}
	who := world.people[1]
	for _, tc := range cases {
		mutated := strings.Replace(original, tc.old, tc.new, 1)
		if mutated == original {
			t.Fatalf("clause %s not found in identity.may", tc.name)
		}
		if _, err := owner.Exec(ctx, mutated); err != nil {
			t.Fatal(err)
		}
		var mismatch bool
		for _, objectType := range domain.ObjectTypes() {
			view, edit := loadShared(t, app, ctx, who, objectType)
			got := rlsReturning(t, app, ctx, who, objectType, view, edit, tc.query)
			wantView, wantEdit, wantDelete := wantIDs(world, who, objectType)
			if !slices.Equal(got, tc.want(wantView, wantEdit, wantDelete)) {
				mismatch = true
				break
			}
		}
		if !mismatch {
			t.Fatalf("dropping the %s clause still agreed with Can", tc.name)
		}
		if _, err := owner.Exec(ctx, original); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppRoleReturnsAfterReset(t *testing.T) {
	owner, ctx := upOwner(t)
	app := pgtest.App(t, owner)
	n := int(app.Config().MaxConns)
	held := make([]*pgxpool.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := app.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(ctx, "RESET ROLE"); err != nil {
			t.Fatal(err)
		}
		var who string
		if err := c.QueryRow(ctx, "SELECT current_user").Scan(&who); err != nil {
			t.Fatal(err)
		}
		if who == "upmo_app" {
			t.Fatal("RESET ROLE did not return to the login user")
		}
		held = append(held, c)
	}
	for _, c := range held {
		c.Release()
	}
	var who string
	if err := app.QueryRow(ctx, "SELECT current_user").Scan(&who); err != nil {
		t.Fatal(err)
	}
	if who != "upmo_app" {
		t.Fatalf("after checkout current_user = %s", who)
	}
}

func TestProtectWorkObjects(t *testing.T) {
	owner, ctx := upOwner(t)
	ownerID := seedOwner(t, owner, ctx, "ada@example.com")
	editorID := insertUser(t, owner, ctx, "noor@example.com")
	if _, err := owner.Exec(ctx, `
		CREATE TABLE work_guard (
			id uuid PRIMARY KEY DEFAULT uuidv7(),
			object_type text NOT NULL,
			owner_user_id uuid NOT NULL,
			visibility text NOT NULL,
			visibility_team_id uuid
		)`); err != nil {
		t.Fatal(err)
	}
	var objectID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO work_guard (object_type, owner_user_id, visibility)
		VALUES ('task', $1, 'private') RETURNING id::text`, ownerID).Scan(&objectID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', $1, $2, 'editor', $3)`, objectID, editorID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `SELECT identity.protect('work_guard')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `SELECT identity.protect('work_guard')`); err != nil {
		t.Fatal("identity.protect is not idempotent: " + err.Error())
	}
	if _, err := owner.Exec(ctx, `SELECT identity.protect('identity.users')`); err == nil {
		t.Fatal("identity.protect accepted an identity table")
	}
	gaps, err := protectionGaps(ctx, owner, "work_guard")
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) > 0 {
		t.Fatalf("after protect: %s", strings.Join(gaps, "; "))
	}
	if _, err := owner.Exec(ctx, `CREATE POLICY extra ON work_guard FOR SELECT USING (true)`); err != nil {
		t.Fatal(err)
	}
	gaps, err = protectionGaps(ctx, owner, "work_guard")
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) == 0 {
		t.Fatal("an extra policy still passed the guard")
	}
	if _, err := owner.Exec(ctx, `DROP POLICY extra ON work_guard`); err != nil {
		t.Fatal(err)
	}

	app := pgtest.App(t, owner)
	ada := hold(t, owner, ctx, "ada", ownerID, "owner", true, false, nil, nil)
	noor := hold(t, owner, ctx, "noor", editorID, "member", true, false, nil, nil)
	view, edit := loadShared(t, app, ctx, noor, domain.TypeTask)
	if err := updateAs(t, app, ctx, noor, view, edit, `UPDATE work_guard SET visibility = 'everyone' WHERE id = $1`, objectID); err != nil {
		t.Fatalf("editor could not edit: %v", err)
	}
	if err := updateAs(t, app, ctx, noor, view, edit, `UPDATE work_guard SET owner_user_id = $1 WHERE id = $2`, editorID, objectID); err == nil {
		t.Fatal("editor took ownership")
	}
	if err := updateAs(t, app, ctx, ada, nil, nil, `UPDATE work_guard SET owner_user_id = $1 WHERE id = $2`, editorID, objectID); err == nil {
		t.Fatal("owner_user_id was updatable")
	}
}

func updateAs(t *testing.T, pool *pgxpool.Pool, ctx context.Context, who held, view, edit []string, query string, args ...any) error {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	applyAccess(t, tx, ctx, who, domain.TypeTask, view, edit)
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
