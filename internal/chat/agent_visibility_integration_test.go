//go:build integration

package chat_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	agOwner    = "10000000-0000-7000-8000-000000000001"
	agAdmin    = "10000000-0000-7000-8000-000000000002"
	agMember   = "10000000-0000-7000-8000-000000000003"
	agViewer   = "10000000-0000-7000-8000-000000000004"
	agCustom   = "10000000-0000-7000-8000-000000000005"
	agStranger = "10000000-0000-7000-8000-000000000006"
	agTeam     = "10000000-0000-7000-8000-000000000007"

	// Member agent permissions plus settings:manage — proves sharing needs manage, not agent verbs alone.
	deskRolePermissions = "agent:create,agent:view,agent:edit,agent:delete,agent:share,settings:manage"
)

type agWant struct{ see, use, write bool }

type agWorld struct {
	t      *testing.T
	owner  *pgxpool.Pool
	app    *pgxpool.Pool
	own    map[string]string
	shared map[string]string
	people map[string]string
}

func agentMigrationsUpTo(t *testing.T, names ...string) fs.FS {
	t.Helper()
	files := fstest.MapFS{}
	for _, name := range names {
		raw, err := fs.ReadFile(chat.Migrations(), name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: raw}
	}
	return files
}

func newAgWorld(t *testing.T) *agWorld {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.New(t)
	// The agent store records audit events, so the audit schema comes up first.
	if err := audit.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	w := &agWorld{
		t: t, owner: pool, app: pgtest.App(t, pool),
		own: map[string]string{}, shared: map[string]string{},
		people: map[string]string{
			"owner": agOwner, "admin": agAdmin, "member": agMember,
			"viewer": agViewer, "custom": agCustom,
		},
	}
	w.exec(`INSERT INTO identity.roles (name, permissions) VALUES ('Desk', $1::text[])`, strings.Split(deskRolePermissions, ","))
	for _, person := range []struct{ id, email, role string }{
		{agOwner, "owner@example.com", "owner"},
		{agAdmin, "admin@example.com", "admin"},
		{agMember, "member@example.com", "member"},
		{agViewer, "viewer@example.com", "viewer"},
		{agCustom, "custom@example.com", "Desk"},
		{agStranger, "stranger@example.com", "member"},
	} {
		w.exec(`
			INSERT INTO identity.users (id, email, name, role_id, joined_at)
			VALUES ($1::uuid, $2, $2, (SELECT id FROM identity.roles WHERE key = $3 OR (key IS NULL AND name = $3)), now())`,
			person.id, person.email, person.role)
	}
	w.exec(`INSERT INTO identity.teams (id, name) VALUES ($1::uuid, 'Finance')`, agTeam)
	for role, owner := range map[string]string{
		"owner": agOwner, "admin": agAdmin, "member": agMember, "viewer": agViewer, "custom": agCustom,
	} {
		w.own[role] = w.insertAgent(owner, "own "+role, "private")
	}
	w.shared["private"] = w.insertAgent(agStranger, "private", "private")
	w.shared["team"] = w.insertAgent(agAdmin, "team", "team", agTeam)
	w.shared["everyone"] = w.insertAgent(agAdmin, "everyone", "everyone")
	return w
}

func (w *agWorld) insertAgent(owner, name, visibility string, teams ...string) string {
	w.t.Helper()
	teamArr := "{}"
	if visibility == "team" && len(teams) > 0 {
		teamArr = "{" + teams[0] + "}"
	}
	var id string
	if err := w.owner.QueryRow(context.Background(), `
		INSERT INTO chat.agents (owner_user_id, name, enabled, visibility, team_ids)
		VALUES ($1::uuid, $2, true, $3, $4::uuid[]) RETURNING id::text`,
		owner, name, visibility, teamArr).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *agWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

func (w *agWorld) allow(role string) string {
	id, err := identity.ParseUserID(w.people[role])
	if err != nil {
		w.t.Fatal(err)
	}
	got, err := identity.Members(w.owner, "", nil, nil).EffectiveAllow(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

func agWantFor(role, kind string, onTeam, enabled bool, allow string) agWant {
	manage := strings.Contains(allow, "settings:manage")
	canView := strings.Contains(allow, "agent:view")
	canEdit := strings.Contains(allow, "agent:edit")
	write := false
	if canEdit {
		switch kind {
		case "own":
			write = true
		case "team", "everyone":
			write = manage
		}
	}
	switch kind {
	case "own":
		// SELECT needs agent:view and may_use (private own) or may_manage.
		// use is identity.may_use (own private), not gated on enabled.
		see := canView
		return agWant{see: see, use: see, write: write}
	case "private":
		return agWant{}
	case "team":
		see := canView && ((onTeam && enabled) || manage)
		return agWant{see: see, use: see && onTeam, write: write}
	case "everyone":
		see := canView && (enabled || manage)
		return agWant{see: see, use: see, write: write}
	default:
		return agWant{}
	}
}

func agInsertWant(allow, visibility string) bool {
	if !strings.Contains(allow, "agent:create") {
		return false
	}
	if visibility == "private" {
		return true
	}
	return strings.Contains(allow, "settings:manage")
}

func (w *agWorld) as(user, allow, teams string, fn func(pgx.Tx)) {
	w.t.Helper()
	ctx := context.Background()
	tx, err := w.app.Begin(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
			set_config('upmo.allow', $2, true),
			set_config('upmo.teams', $3, true)`, user, allow, teams); err != nil {
		w.t.Fatal(err)
	}
	fn(tx)
}

func (w *agWorld) touched(tx pgx.Tx, sql string, args ...any) (int64, error) {
	w.t.Helper()
	ctx := context.Background()
	if _, err := tx.Exec(ctx, "SAVEPOINT ag_write"); err != nil {
		w.t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT ag_write"); rbErr != nil {
		w.t.Fatal(rbErr)
	}
	return tag.RowsAffected(), err
}

func agAllowed(n int64, err error) bool { return err == nil && n == 1 }

func TestAgentAccessMatrix(t *testing.T) {
	w := newAgWorld(t)
	ctx := context.Background()
	store := postgres.Store{Pool: w.app}
	kinds := []string{"own", "private", "team", "everyone"}
	roles := []string{"owner", "admin", "member", "viewer", "custom"}
	for _, role := range roles {
		for _, onTeam := range []bool{true, false} {
			teams := "{}"
			if onTeam {
				teams = "{" + agTeam + "}"
			}
			for _, kind := range kinds {
				if kind != "team" && !onTeam {
					continue
				}
				for _, enabled := range []bool{true, false} {
					if !enabled && kind != "own" && kind != "everyone" && kind != "team" {
						continue
					}
					id := w.own[role]
					if kind != "own" {
						id = w.shared[kind]
					}
					w.exec(`UPDATE chat.agents SET enabled = $2 WHERE id = $1::uuid`, id, enabled)
					want := agWantFor(role, kind, onTeam, enabled, w.allow(role))
					where := role + " " + kind
					if kind == "team" && onTeam {
						where += " on team"
					} else if kind == "team" {
						where += " off team"
					}
					if !enabled {
						where += " disabled"
					}
					access := app.Access{UserID: w.people[role], Allow: w.allow(role), Teams: teams}
					got, getErr := store.GetAgent(ctx, access, id)
					switch {
					case want.use:
						if getErr != nil || !got.Share.CanUse {
							t.Fatalf("%s: can_use err=%v got=%v want true", where, getErr, got.Share.CanUse)
						}
					case want.see:
						if getErr != nil || got.Share.CanUse {
							t.Fatalf("%s: can_use err=%v got=%v want false", where, getErr, got.Share.CanUse)
						}
					default:
						if getErr == nil {
							t.Fatalf("%s: got agent without see", where)
						}
					}
					w.as(w.people[role], w.allow(role), teams, func(tx pgx.Tx) {
						var n int
						if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat.agents WHERE id = $1::uuid`, id).Scan(&n); err != nil {
							t.Fatal(err)
						}
						if (n == 1) != want.see {
							t.Fatalf("%s: see %d want %v", where, n, want.see)
						}
						n64, err := w.touched(tx, `UPDATE chat.agents SET description = description WHERE id = $1::uuid`, id)
						if agAllowed(n64, err) != want.write {
							t.Fatalf("%s: update %d %v want write %v", where, n64, err, want.write)
						}
						n64, err = w.touched(tx, `DELETE FROM chat.agents WHERE id = $1::uuid`, id)
						if agAllowed(n64, err) != want.write {
							t.Fatalf("%s: delete %d %v want write %v", where, n64, err, want.write)
						}
						vis := kindVisibility(kind)
						insertWant := agInsertWant(w.allow(role), vis)
						n64, err = w.touched(tx, `
							INSERT INTO chat.agents (owner_user_id, name, visibility, team_ids)
							VALUES ($1::uuid, $2, $3, $4::uuid[])`,
							w.people[role], "new "+where, vis, teamInsert(kind))
						if agAllowed(n64, err) != insertWant {
							t.Fatalf("%s: insert %d %v want %v", where, n64, err, insertWant)
						}
					})
				}
			}
		}
	}
	insertSQL := `
		INSERT INTO chat.agents (owner_user_id, name, visibility, team_ids)
		VALUES ($1::uuid, $2, $3, $4::uuid[])`
	for _, role := range roles {
		for _, visibility := range []string{"private", "team", "everyone"} {
			want := agInsertWant(w.allow(role), visibility)
			teamArg := teamIDsForVisibility(visibility)
			where := role + " insert " + visibility
			w.as(w.people[role], w.allow(role), "{}", func(tx pgx.Tx) {
				n64, err := w.touched(tx, insertSQL,
					w.people[role], "new "+where, visibility, teamArg)
				if agAllowed(n64, err) != want {
					t.Fatalf("%s = %d, %v, want %v", where, n64, err, want)
				}
				if !want && (err == nil || !strings.Contains(err.Error(), "row-level security")) {
					t.Fatalf("%s: want row security, got %v", where, err)
				}
				n64, err = w.touched(tx, insertSQL,
					agStranger, "other "+where, visibility, teamArg)
				if err == nil || !strings.Contains(err.Error(), "row-level security") {
					t.Fatalf("%s owned by the caller only: %d, %v", where, n64, err)
				}
			})
		}
	}
}

func kindVisibility(kind string) string {
	if kind == "own" || kind == "private" {
		return "private"
	}
	return kind
}

func teamInsert(kind string) string {
	return teamIDsForVisibility(kindVisibility(kind))
}

func teamIDsForVisibility(visibility string) string {
	if visibility == "team" {
		return "{" + agTeam + "}"
	}
	return "{}"
}

func TestAViewerCannotCreateButCanUseAWorkspaceAgent(t *testing.T) {
	w := newAgWorld(t)
	store := postgres.Store{Pool: w.app}
	ctx := context.Background()
	viewer := app.Access{UserID: agViewer, Allow: w.allow("viewer"), Teams: "{}"}
	if _, err := app.CreateAgent(ctx, store, nil, viewer, app.AgentFields{Name: "Nope", Enabled: true}); !errors.Is(err, app.ErrAgentCreate) {
		t.Fatalf("viewer create: %v", err)
	}
	got, err := store.GetAgent(ctx, viewer, w.shared["everyone"])
	if err != nil || !got.Share.CanUse {
		t.Fatalf("viewer uses workspace agent: %+v %v", got, err)
	}
}

type agentTeamLookup struct {
	members identity.MemberService
}

func (a agentTeamLookup) TeamNames(ctx context.Context, teamIDs []string) (map[string]string, error) {
	_, names, err := a.members.NamesOf(ctx, nil, teamIDs)
	return names, err
}

func TestMemberAgentVerbsWithoutManageCannotShareButDeskCan(t *testing.T) {
	w := newAgWorld(t)
	update := `UPDATE chat.agents SET description = description WHERE id = $1::uuid`
	insert := `
		INSERT INTO chat.agents (owner_user_id, name, visibility, team_ids)
		VALUES ($1::uuid, $2, $3, $4::uuid[])`
	for _, tc := range []struct {
		role, vis, teams, teamArg string
		agentID                   string
		want                      bool
	}{
		{"member", "everyone", "{}", "{}", w.shared["everyone"], false},
		{"member", "team", "{" + agTeam + "}", "{" + agTeam + "}", w.shared["team"], false},
		{"custom", "everyone", "{}", "{}", w.shared["everyone"], true},
		{"custom", "team", "{" + agTeam + "}", "{" + agTeam + "}", w.shared["team"], true},
	} {
		where := tc.role + " " + tc.vis
		w.as(w.people[tc.role], w.allow(tc.role), tc.teams, func(tx pgx.Tx) {
			n, err := w.touched(tx, update, tc.agentID)
			if agAllowed(n, err) != tc.want {
				t.Fatalf("%s update: %d %v want %v", where, n, err, tc.want)
			}
			n, err = w.touched(tx, insert, w.people[tc.role], "new "+where, tc.vis, tc.teamArg)
			if agAllowed(n, err) != tc.want {
				t.Fatalf("%s insert: %d %v want %v", where, n, err, tc.want)
			}
		})
	}
}

func TestAMemberCannotShareAndAnAdminCan(t *testing.T) {
	w := newAgWorld(t)
	ctx := context.Background()
	members := identity.Members(w.owner, "", nil, nil)
	teams := agentTeamLookup{members: members}
	store := postgres.Store{Pool: w.app}
	try := func(access app.Access, name, vis string, teamIDs []string) error {
		_, err := app.CreateAgent(ctx, store, teams, access, app.AgentFields{
			Name: name, Enabled: true, Visibility: vis, TeamIDs: teamIDs,
		})
		return err
	}
	member := app.Access{UserID: agMember, Allow: w.allow("member"), Teams: "{}"}
	if err := try(member, "member private", domain.VisibilityPrivate, nil); err != nil {
		t.Fatalf("member private: %v", err)
	}
	if err := try(member, "member everyone", domain.VisibilityEveryone, nil); err == nil {
		t.Fatal("member shared")
	}
	if err := try(member, "member team", domain.VisibilityTeam, []string{agTeam}); err == nil {
		t.Fatal("member team share")
	}
	admin := app.Access{UserID: agAdmin, Allow: w.allow("admin"), Teams: "{}"}
	if err := try(admin, "admin everyone", domain.VisibilityEveryone, nil); err != nil {
		t.Fatalf("admin everyone: %v", err)
	}
	if err := try(admin, "admin team", domain.VisibilityTeam, []string{agTeam}); err != nil {
		t.Fatalf("admin team: %v", err)
	}
}

func TestOnlyTheOwnerTakesASharedAgentPrivate(t *testing.T) {
	w := newAgWorld(t)
	ctx := context.Background()
	stmt := `UPDATE chat.agents SET visibility = 'private', team_ids = '{}' WHERE id = $1::uuid`
	for _, role := range []string{"owner", "admin", "member", "viewer", "custom"} {
		id := w.insertAgent(w.people[role], "everyone "+role, "everyone")
		w.as(w.people[role], w.allow(role), "{}", func(tx pgx.Tx) {
			n, err := w.touched(tx, stmt, id)
			allow := w.allow(role)
			want := strings.Contains(allow, "settings:manage") && strings.Contains(allow, "agent:edit")
			if agAllowed(n, err) != want {
				t.Errorf("%s took private: %d %v want %v", role, n, err, want)
				return
			}
			if !want {
				return
			}
			if _, err := tx.Exec(ctx, stmt, id); err != nil {
				t.Errorf("%s private update: %v", role, err)
				return
			}
			var vis string
			var emptyTeams bool
			if err := tx.QueryRow(ctx, `
				SELECT visibility, team_ids = '{}'::uuid[]
				FROM chat.agents WHERE id = $1::uuid`, id).Scan(&vis, &emptyTeams); err != nil {
				t.Errorf("%s read back: %v", role, err)
				return
			}
			if vis != "private" || !emptyTeams {
				t.Errorf("%s after private: vis %q emptyTeams %v", role, vis, emptyTeams)
			}
		})
	}

	memberShared := w.insertAgent(w.people["member"], "everyone member shared", "everyone")
	w.as(w.people["admin"], w.allow("admin"), "{}", func(tx pgx.Tx) {
		n, err := w.touched(tx, stmt, memberShared)
		if agAllowed(n, err) {
			t.Errorf("admin took member shared private: %d %v want denied", n, err)
		}
	})
	var vis string
	var emptyTeams bool
	if err := w.owner.QueryRow(ctx, `
		SELECT visibility, team_ids = '{}'::uuid[]
		FROM chat.agents WHERE id = $1::uuid`, memberShared).Scan(&vis, &emptyTeams); err != nil {
		t.Fatal(err)
	}
	if vis != "everyone" || !emptyTeams {
		t.Fatalf("member shared stayed shared: vis %q emptyTeams %v", vis, emptyTeams)
	}
}

func TestAnAdminCannotSeeAnotherPersonsPrivateAgent(t *testing.T) {
	w := newAgWorld(t)
	store := postgres.Store{Pool: w.app}
	ctx := context.Background()
	admin := app.Access{UserID: agAdmin, Allow: w.allow("admin"), Teams: "{}"}
	if _, err := store.GetAgent(ctx, admin, w.shared["private"]); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("admin saw private: %v", err)
	}
}

func TestBackfillGivesOldGlobalAgentsAnOwnerAndEveryone(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	upTo5 := agentMigrationsUpTo(t, "00001_chat.sql", "00002_chat_lines.sql", "00003_chat_stream.sql", "00004_chat_picks.sql", "00005_chat_agents.sql")
	if err := migrate.Up(ctx, pool, chat.Schema, upTo5); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO chat.agents (owner_user_id, name) VALUES (NULL, 'Legacy')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err == nil || !strings.Contains(err.Error(), "chat agents need a joined owner") {
		t.Fatalf("up with no owner: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM chat.agents WHERE name = 'Legacy'`); err != nil {
		t.Fatal(err)
	}
	var backfillOwner string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('early@example.com', 'early', (SELECT id FROM identity.roles WHERE key = 'owner'), '2020-01-02')
		RETURNING id::text`).Scan(&backfillOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO chat.agents (owner_user_id, name) VALUES (NULL, 'Legacy')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	var owner, vis string
	if err := pool.QueryRow(ctx, `SELECT owner_user_id::text, visibility FROM chat.agents WHERE name = 'Legacy'`).Scan(&owner, &vis); err != nil {
		t.Fatal(err)
	}
	if owner != backfillOwner || vis != "everyone" {
		t.Fatalf("backfill = owner %q vis %q", owner, vis)
	}
	if err := migrate.Down(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
}

func TestChangingTeamAgentToEveryoneOrPrivateClearsTeamIDs(t *testing.T) {
	w := newAgWorld(t)
	ctx := context.Background()
	members := identity.Members(w.owner, "", nil, nil)
	teams := agentTeamLookup{members: members}
	store := postgres.Store{Pool: w.app}
	admin := app.Access{UserID: agAdmin, Allow: w.allow("admin"), Teams: "{}"}
	readRow := func(id string) (vis string, emptyTeams bool) {
		if err := w.owner.QueryRow(ctx, `
			SELECT visibility, team_ids = '{}'::uuid[]
			FROM chat.agents WHERE id = $1::uuid`, id).Scan(&vis, &emptyTeams); err != nil {
			t.Fatal(err)
		}
		return vis, emptyTeams
	}
	everyone := domain.VisibilityEveryone
	if _, err := app.ChangeAgent(ctx, store, teams, admin, w.shared["team"], app.AgentChange{Visibility: &everyone}); err != nil {
		t.Fatalf("team to everyone: %v", err)
	}
	if vis, empty := readRow(w.shared["team"]); vis != "everyone" || !empty {
		t.Fatalf("after everyone: vis %q emptyTeams %v", vis, empty)
	}
	teamPrivate := w.insertAgent(agAdmin, "team private", "team", agTeam)
	private := domain.VisibilityPrivate
	if _, err := app.ChangeAgent(ctx, store, teams, admin, teamPrivate, app.AgentChange{Visibility: &private}); err != nil {
		t.Fatalf("team to private: %v", err)
	}
	if vis, empty := readRow(teamPrivate); vis != "private" || !empty {
		t.Fatalf("after private: vis %q emptyTeams %v", vis, empty)
	}
}

func TestATeamChangeTakesEffectOnTheNextRequest(t *testing.T) {
	w := newAgWorld(t)
	ctx := context.Background()
	w.exec(`INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, agTeam, agMember)
	store := postgres.Store{Pool: w.app}
	member := app.Access{UserID: agMember, Allow: w.allow("member"), Teams: "{}"}
	if _, err := store.GetAgent(ctx, member, w.shared["team"]); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("before join: %v", err)
	}
	member.Teams = "{" + agTeam + "}"
	if _, err := store.GetAgent(ctx, member, w.shared["team"]); err != nil {
		t.Fatalf("after join: %v", err)
	}
}

// The reply's look-up is stricter than the picker's: a manager sees a team
// agent they are not on, but a reply must not run it for them.
func TestAReplyRunsOnlyAgentsThePersonMayUse(t *testing.T) {
	w := newAgWorld(t)
	store := postgres.Store{Pool: w.app}
	ctx := context.Background()
	manager := app.Access{UserID: agOwner, Allow: w.allow("owner"), Teams: "{}"}
	if _, err := store.GetAgent(ctx, manager, w.shared["team"]); err != nil {
		t.Fatalf("a manager off the team still sees the team agent: %v", err)
	}
	if _, err := store.Agent(ctx, manager, w.shared["team"]); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("a manager off the team runs a team agent: %v", err)
	}
	member := app.Access{UserID: agMember, Allow: w.allow("member"), Teams: "{" + agTeam + "}"}
	if _, err := store.Agent(ctx, member, w.shared["team"]); err != nil {
		t.Fatalf("a member on the team: %v", err)
	}
	mine := app.Access{UserID: agStranger, Allow: w.allow("member"), Teams: "{}"}
	if _, err := store.Agent(ctx, mine, w.shared["private"]); err != nil {
		t.Fatalf("the owner of a private agent: %v", err)
	}
	if _, err := store.Agent(ctx, member, w.shared["private"]); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("another person on a private agent: %v", err)
	}
}
