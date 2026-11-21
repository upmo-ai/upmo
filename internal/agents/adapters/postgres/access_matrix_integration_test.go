//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/agents/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/agents/app"
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

func newAgWorld(t *testing.T) *agWorld {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := audit.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, agents.Schema, agents.Migrations()); err != nil {
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
			INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3 OR (key IS NULL AND name = $3)), now())`,
			person.id, person.email, person.role)
	}
	w.exec(`INSERT INTO identity.teams (id, name) VALUES ($1::uuid, 'Finance')`, agTeam)
	for role, owner := range map[string]string{
		"owner": agOwner, "admin": agAdmin, "member": agMember, "viewer": agViewer, "custom": agCustom,
	} {
		w.own[role] = w.insertAgent(owner, "own "+role, "own-"+role, "private")
	}
	w.shared["private"] = w.insertAgent(agStranger, "private", "private", "private")
	w.shared["team"] = w.insertAgent(agAdmin, "team", "team", "team", agTeam)
	w.shared["everyone"] = w.insertAgent(agAdmin, "everyone", "everyone", "everyone")
	return w
}

func (w *agWorld) insertAgent(owner, name, slug, visibility string, teams ...string) string {
	w.t.Helper()
	teamArr := "{}"
	if visibility == "team" && len(teams) > 0 {
		teamArr = "{" + teams[0] + "}"
	}
	var id string
	if err := w.owner.QueryRow(context.Background(), `
		INSERT INTO agents.agents (owner_user_id, name, slug, enabled, visibility, team_ids)
		VALUES ($1::uuid, $2, $3, true, $4, $5::uuid[]) RETURNING id::text`,
		owner, name, slug, visibility, teamArr).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	w.exec(`INSERT INTO agents.versions (agent_id, version, digest, snapshot, created_by)
		VALUES ($1::uuid, 1, repeat('a', 64), '{}'::jsonb, $2::uuid)`, id, owner)
	w.exec(`INSERT INTO agents.lessons (agent_id, title, cue, body, always_inline)
		VALUES ($1::uuid, 'Rule 1', 'Always', 'Follow rule 1', true)`, id)
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
	got, err := identity.Members(w.owner, nil, "", nil, nil).EffectiveAllow(context.Background(), id)
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

func TestAgentAccessMatrixAcrossTables(t *testing.T) {
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
					w.exec(`UPDATE agents.agents SET enabled = $2 WHERE id = $1::uuid`, id, enabled)
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
						var n, vn, ln int
						if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents.agents WHERE id = $1::uuid`, id).Scan(&n); err != nil {
							t.Fatal(err)
						}
						if (n == 1) != want.see {
							t.Fatalf("%s: see %d want %v", where, n, want.see)
						}
						if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents.versions WHERE agent_id = $1::uuid`, id).Scan(&vn); err != nil {
							t.Fatal(err)
						}
						if (vn == 1) != want.see {
							t.Fatalf("%s: versions see %d want %v", where, vn, want.see)
						}
						if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents.lessons WHERE agent_id = $1::uuid`, id).Scan(&ln); err != nil {
							t.Fatal(err)
						}
						if (ln == 1) != want.see {
							t.Fatalf("%s: lessons see %d want %v", where, ln, want.see)
						}
						n64, err := w.touched(tx, `UPDATE agents.agents SET description = description WHERE id = $1::uuid`, id)
						if agAllowed(n64, err) != want.write {
							t.Fatalf("%s: update %d %v want write %v", where, n64, err, want.write)
						}
						n64, err = w.touched(tx, `DELETE FROM agents.agents WHERE id = $1::uuid`, id)
						if agAllowed(n64, err) != want.write {
							t.Fatalf("%s: delete %d %v want write %v", where, n64, err, want.write)
						}
						n64, err = w.touched(tx, `INSERT INTO agents.versions (agent_id, version, digest, snapshot) VALUES ($1::uuid, 99, repeat('b', 64), '{}'::jsonb)`, id)
						if agAllowed(n64, err) != want.write {
							t.Fatalf("%s: insert version %d %v want write %v", where, n64, err, want.write)
						}
						n64, err = w.touched(tx, `UPDATE agents.lessons SET cue = cue WHERE agent_id = $1::uuid`, id)
						if agAllowed(n64, err) != want.write {
							t.Fatalf("%s: update lesson %d %v want write %v", where, n64, err, want.write)
						}
						vis := "private"
						teamArg := "{}"
						if kind == "team" || kind == "everyone" {
							vis = kind
						}
						if vis == "team" {
							teamArg = "{" + agTeam + "}"
						}
						insertWant := agInsertWant(w.allow(role), vis)
						slug := "new-" + strings.ReplaceAll(where, " ", "-")
						n64, err = w.touched(tx, `
							INSERT INTO agents.agents (owner_user_id, name, slug, visibility, team_ids)
							VALUES ($1::uuid, $2, $3, $4, $5::uuid[])`,
							w.people[role], "new "+where, slug, vis, teamArg)
						if agAllowed(n64, err) != insertWant {
							t.Fatalf("%s: insert %d %v want %v", where, n64, err, insertWant)
						}
					})
				}
			}
		}
	}
}

func TestAppRoleCannotMarkAnAgentInstalled(t *testing.T) {
	w := newAgWorld(t)
	allow := w.allow("admin")
	own := w.own["admin"]
	w.as(agAdmin, allow, "{}", func(tx pgx.Tx) {
		_, err := w.touched(tx, `UPDATE agents.agents SET installed = true WHERE id = $1::uuid`, own)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("set installed: %v, want permission denied (42501)", err)
		}
		n, err := w.touched(tx, `INSERT INTO agents.agents (owner_user_id, name, slug, installed)
			VALUES ($1::uuid, 'Forged', 'forged', true)`, agAdmin)
		if agAllowed(n, err) {
			t.Fatal("app role inserted an installed agent")
		}
		if n, err := w.touched(tx, `UPDATE agents.agents SET enabled = false WHERE id = $1::uuid`, own); !agAllowed(n, err) {
			t.Fatalf("plain edit: %d, %v, want allowed", n, err)
		}
	})

	var installed string
	if err := w.owner.QueryRow(context.Background(), `
		INSERT INTO agents.agents (owner_user_id, name, slug, enabled, installed)
		VALUES ($1::uuid, 'Packed', 'packed', true, true) RETURNING id::text`, agAdmin).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	w.as(agAdmin, allow, "{}", func(tx pgx.Tx) {
		if n, err := w.touched(tx, `UPDATE agents.agents SET enabled = false WHERE id = $1::uuid`, installed); !agAllowed(n, err) {
			t.Fatalf("edit installed agent: %d, %v, want allowed", n, err)
		}
		for _, value := range []string{"false", "true"} {
			_, err := w.touched(tx, `UPDATE agents.agents SET installed = `+value+` WHERE id = $1::uuid`, installed)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Fatalf("set installed = %s on installed agent: %v, want permission denied (42501)", value, err)
			}
		}
	})
}
