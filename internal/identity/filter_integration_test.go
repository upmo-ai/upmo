//go:build integration

package identity_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestListFilterAgreesWithCan(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			pool, ctx := up(t)
			if _, err := pool.Exec(ctx, `
				CREATE TABLE work_objects (
					id uuid PRIMARY KEY DEFAULT uuidv7(),
					object_type text NOT NULL,
					owner_user_id uuid NOT NULL,
					visibility text NOT NULL,
					visibility_team_id uuid
				)`); err != nil {
				t.Fatal(err)
			}
			world := buildWorld(t, pool, ctx, seed)
			for _, subject := range world.subjects() {
				for _, objectType := range domain.ObjectTypes() {
					agree(t, pool, ctx, world, subject, objectType)
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
	return out
}

func buildWorld(t *testing.T, pool *pgxpool.Pool, ctx context.Context, seed uint64) world {
	t.Helper()
	owner := seedOwner(t, pool, ctx, "owner@example.com")
	rng := rand.New(rand.NewPCG(seed, seed+1))

	type seat struct {
		email, roleKey   string
		joined, disabled bool
		custom           []string
	}
	extra := []seat{
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
	for i, seat := range extra {
		user := insertUser(t, pool, ctx, seat.email)
		var role string
		if seat.custom != nil {
			if err := pool.QueryRow(ctx, `INSERT INTO identity.roles (name, permissions) VALUES ($1, $2) RETURNING id`, fmt.Sprintf("Custom %d", i), seat.custom).Scan(&role); err != nil {
				t.Fatal(err)
			}
		}
		people = append(people, hold(t, pool, ctx, fmt.Sprintf("p%d", i), user, seat.roleKey, seat.joined, seat.disabled, seat.custom, &role))
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
	teams := make([]string, len(who.subject.Teams))
	for i, team := range who.subject.Teams {
		teams[i] = team.String()
	}
	sharedRows, err := pool.Query(ctx, domain.SharedObjectsSQL(), string(objectType), who.subject.UserID.String(), teams, who.subject.RoleID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer sharedRows.Close()
	var shared []string
	for sharedRows.Next() {
		var id string
		if err := sharedRows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		shared = append(shared, id)
	}
	if err := sharedRows.Err(); err != nil {
		t.Fatal(err)
	}
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
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, 'Person', (SELECT id FROM identity.roles WHERE key = 'member'), now()) RETURNING id::text`, email).Scan(&id); err != nil {
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
