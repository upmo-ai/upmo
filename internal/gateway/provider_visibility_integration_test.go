//go:build integration

package gateway

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// Fixed people for the access matrix. The owner of the team account is the
// admin, who is not on that team unless a case says so.
const (
	visOwner    = "10000000-0000-7000-8000-000000000001"
	visAdmin    = "10000000-0000-7000-8000-000000000002"
	visMember   = "10000000-0000-7000-8000-000000000003"
	visViewer   = "10000000-0000-7000-8000-000000000004"
	visCustom   = "10000000-0000-7000-8000-000000000005"
	visStranger = "10000000-0000-7000-8000-000000000006"
	visTeam     = "10000000-0000-7000-8000-000000000007"
)

// visWant is what one person may do with one account.
type visWant struct {
	see, use, write, key, modelWrite bool
}

// visAccess is the matrix. see is the row policy (use or manage). use is
// identity.can_use, which is what the gateway routes on. write is an account
// update or delete. A house account is not written here. modelWrite follows
// may_manage, including a house account.
func visAccess(role, kind string, onTeam bool) visWant {
	manage := role == "owner" || role == "admin" || role == "custom"
	switch kind {
	case "own":
		return visWant{see: true, use: true, write: true, key: true, modelWrite: true}
	case "private":
		return visWant{}
	case "team":
		return visWant{see: onTeam || manage, use: onTeam, write: manage, key: manage, modelWrite: manage}
	case "everyone":
		return visWant{see: true, use: true, write: manage, key: manage, modelWrite: manage}
	case "house":
		return visWant{see: true, use: true, modelWrite: manage}
	default:
		return visWant{}
	}
}

type visAccount struct {
	id, ref, owner, visibility string
	house                      bool
}

type visWorld struct {
	t     *testing.T
	owner *pgxpool.Pool
	app   *pgxpool.Pool
	// accounts by kind. "own" is filled per person.
	shared map[string]visAccount
	own    map[string]visAccount
	people map[string]string
}

func newVisWorld(t *testing.T) *visWorld {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		t.Fatal(err)
	}
	w := &visWorld{
		t: t, owner: pool, app: pgtest.App(t, pool),
		shared: map[string]visAccount{}, own: map[string]visAccount{},
		people: map[string]string{
			"owner": visOwner, "admin": visAdmin, "member": visMember,
			"viewer": visViewer, "custom": visCustom,
		},
	}
	w.exec(`
		INSERT INTO identity.roles (name, permissions)
		VALUES ('Desk', '{settings:manage}')`)
	for _, person := range []struct{ id, email, role string }{
		{visOwner, "owner@example.com", "owner"},
		{visAdmin, "admin@example.com", "admin"},
		{visMember, "member@example.com", "member"},
		{visViewer, "viewer@example.com", "viewer"},
		{visCustom, "custom@example.com", "Desk"},
		{visStranger, "stranger@example.com", "member"},
	} {
		w.exec(`
			INSERT INTO identity.users (id, email, name, role_id, joined_at)
			VALUES ($1::uuid, $2, $2, (SELECT id FROM identity.roles WHERE key = $3 OR (key IS NULL AND name = $3)), now())`,
			person.id, person.email, person.role)
	}
	w.exec(`INSERT INTO identity.teams (id, name) VALUES ($1::uuid, 'Finance')`, visTeam)
	w.add(visAccount{id: "20000000-0000-7000-8000-000000000001", ref: "ref_stranger", owner: visStranger, visibility: "private"}, "private")
	w.add(visAccount{id: "20000000-0000-7000-8000-000000000002", ref: "ref_team", owner: visAdmin, visibility: "team"}, "team")
	w.add(visAccount{id: "20000000-0000-7000-8000-000000000003", ref: "ref_everyone", owner: visAdmin, visibility: "everyone"}, "everyone")
	w.add(visAccount{id: "20000000-0000-7000-8000-000000000004", ref: "ref_house", visibility: "everyone", house: true}, "house")
	w.own = map[string]visAccount{
		"owner":  {id: "30000000-0000-7000-8000-000000000001", ref: "ref_own_owner", owner: visOwner, visibility: "private"},
		"admin":  {id: "30000000-0000-7000-8000-000000000002", ref: "ref_own_admin", owner: visAdmin, visibility: "private"},
		"member": {id: "30000000-0000-7000-8000-000000000003", ref: "ref_own_member", owner: visMember, visibility: "private"},
		"viewer": {id: "30000000-0000-7000-8000-000000000004", ref: "ref_own_viewer", owner: visViewer, visibility: "private"},
		"custom": {id: "30000000-0000-7000-8000-000000000005", ref: "ref_own_custom", owner: visCustom, visibility: "private"},
	}
	for _, account := range w.own {
		w.insertAccount(account)
	}
	return w
}

func (w *visWorld) add(account visAccount, kind string) {
	w.t.Helper()
	w.insertAccount(account)
	w.shared[kind] = account
}

func (w *visWorld) insertAccount(account visAccount) {
	w.t.Helper()
	providerName := "acme"
	teams := "{}"
	if account.visibility == "team" {
		providerName = "routed"
		teams = "{" + visTeam + "}"
	}
	w.exec(`
		INSERT INTO gateway.provider_accounts
			(id, owner_user_id, name, adapter, credential_ref, plan_kind, house, provider, visibility, team_ids)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, 'fake', $4, 'metered', $5, $6, $7, $8::uuid[])`,
		account.id, account.owner, account.ref, account.ref, account.house, providerName, account.visibility, teams)
	w.exec(`
		INSERT INTO gateway.provider_models (account_id, model, position)
		VALUES ($1::uuid, 'm', 1)`, account.id)
	w.exec(`
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id)
		VALUES ($1, decode('00', 'hex'), decode('000000000000000000000000', 'hex'), 'k')`, account.ref)
}

func (w *visWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

func (w *visWorld) allow(role string) string {
	if role == "owner" || role == "admin" || role == "custom" {
		return "settings:manage"
	}
	if role == "member" {
		return "task:view"
	}
	return ""
}

func (w *visWorld) account(role, kind string) visAccount {
	if kind == "own" {
		return w.own[role]
	}
	return w.shared[kind]
}

// as runs one request: upmo_app, with the person, their permission and their
// teams named for that transaction only. It rolls the transaction back.
func (w *visWorld) as(user, allow, teams string, fn func(pgx.Tx)) {
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

func (w *visWorld) count(tx pgx.Tx, sql string, args ...any) int {
	w.t.Helper()
	var n int
	if err := tx.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// touched runs one write and undoes it, so a refusal does not abort the
// transaction and a success does not remove the fixture row.
func (w *visWorld) touched(tx pgx.Tx, sql string, args ...any) (int64, error) {
	w.t.Helper()
	ctx := context.Background()
	if _, err := tx.Exec(ctx, "SAVEPOINT vis_write"); err != nil {
		w.t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT vis_write"); rbErr != nil {
		w.t.Fatal(rbErr)
	}
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func TestProviderAccountAccessMatrix(t *testing.T) {
	w := newVisWorld(t)
	ctx := context.Background()
	kinds := []string{"own", "private", "team", "everyone", "house"}
	roles := []string{"owner", "admin", "member", "viewer", "custom"}
	for _, role := range roles {
		for _, onTeam := range []bool{true, false} {
			teams := "{}"
			if onTeam {
				teams = "{" + visTeam + "}"
			}
			for _, kind := range kinds {
				// "on the team" only changes a team account.
				if kind != "team" && !onTeam {
					continue
				}
				want := visAccess(role, kind, onTeam)
				account := w.account(role, kind)
				where := role + " " + kind
				if kind == "team" && !onTeam {
					where += " off the team"
				}
				if kind == "team" && onTeam {
					where += " on the team"
				}
				check := func() {
					var can, may bool
					if err := w.owner.QueryRow(ctx, `
						SELECT identity.can_use($1::uuid, $2::uuid[], owner_user_id, visibility, team_ids)
						FROM gateway.provider_accounts WHERE id = $3::uuid`,
						w.people[role], teams, account.id).Scan(&can); err != nil {
						t.Fatal(err)
					}
					routed := w.countOwner(`
						SELECT count(*) FROM gateway.provider_accounts AS a
						WHERE a.id = $3::uuid
						  AND identity.can_use($1::uuid, $2::uuid[], a.owner_user_id, a.visibility, a.team_ids)`,
						w.people[role], teams, account.id)
					if can != want.use || (routed == 1) != want.use {
						t.Fatalf("%s: can_use = %v routed = %d, want use %v", where, can, routed, want.use)
					}
					w.as(w.people[role], w.allow(role), teams, func(tx pgx.Tx) {
						if err := tx.QueryRow(ctx, `
							SELECT identity.may_use(owner_user_id, visibility, team_ids)
							FROM gateway.provider_accounts WHERE id = $1::uuid`, account.id).Scan(&may); err != nil {
							// A hidden row has no may_use to read. Ask with the
							// arguments instead, which is what the policy calls.
							if err = tx.QueryRow(ctx, `
								SELECT identity.may_use($1::uuid, $2, $3::uuid[])`,
								nullUUID(account.owner), account.visibility, teamArg(account)).Scan(&may); err != nil {
								t.Fatal(err)
							}
						}
						if may != can {
							t.Fatalf("%s: may_use = %v, can_use = %v", where, may, can)
						}
						seen := w.count(tx, `SELECT count(*) FROM gateway.provider_accounts WHERE id = $1::uuid`, account.id)
						if (seen == 1) != want.see {
							t.Fatalf("%s: select = %d, want see %v", where, seen, want.see)
						}
						models := w.count(tx, `SELECT count(*) FROM gateway.provider_models WHERE account_id = $1::uuid`, account.id)
						if (models == 1) != want.see {
							t.Fatalf("%s: models select = %d, want see %v", where, models, want.see)
						}
						keys := w.count(tx, `SELECT count(*) FROM gateway.credentials WHERE ref = $1`, account.ref)
						if (keys == 1) != want.key {
							t.Fatalf("%s: key = %d, want %v", where, keys, want.key)
						}
						n, err := w.touched(tx, `UPDATE gateway.provider_accounts SET name = name WHERE id = $1::uuid`, account.id)
						if allowed(n, err) != want.write {
							t.Fatalf("%s: update = %d, %v, want write %v", where, n, err, want.write)
						}
						n, err = w.touched(tx, `DELETE FROM gateway.provider_accounts WHERE id = $1::uuid`, account.id)
						if allowed(n, err) != want.write {
							t.Fatalf("%s: delete = %d, %v, want write %v", where, n, err, want.write)
						}
						n, err = w.touched(tx, `
							INSERT INTO gateway.provider_models (account_id, model, position)
							VALUES ($1::uuid, 'extra', 2)`, account.id)
						if (err == nil && n == 1) != want.modelWrite {
							t.Fatalf("%s: model insert = %d, %v, want %v", where, n, err, want.modelWrite)
						}
						n, err = w.touched(tx, `DELETE FROM gateway.provider_models WHERE account_id = $1::uuid AND model = 'm'`, account.id)
						if allowed(n, err) != want.modelWrite {
							t.Fatalf("%s: model delete = %d, %v, want %v", where, n, err, want.modelWrite)
						}
					})
				}
				check()
			}
		}
	}
	// A new account: private when the caller owns it, team and everyone only
	// with settings:manage, never house. The row's owner is the caller.
	insert := `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, name, adapter, credential_ref, plan_kind, house, visibility, team_ids)
		VALUES (NULLIF($1, '')::uuid, $2, 'fake', $3, 'local', $4, $5, $6::uuid[])`
	for _, role := range roles {
		manage := role == "owner" || role == "admin" || role == "custom"
		for _, visibility := range []string{"private", "team", "everyone"} {
			want := visibility == "private" || manage
			teamIDs := "{}"
			if visibility == "team" {
				teamIDs = "{" + visTeam + "}"
			}
			where := role + " insert " + visibility
			w.as(w.people[role], w.allow(role), "{}", func(tx pgx.Tx) {
				n, err := w.touched(tx, insert,
					w.people[role], "new "+where, "ref_new_"+role+"_"+visibility, false, visibility, teamIDs)
				if allowed(n, err) != want {
					t.Fatalf("%s = %d, %v, want %v", where, n, err, want)
				}
				if !want && (err == nil || !strings.Contains(err.Error(), "row-level security")) {
					t.Fatalf("%s: want row security, got %v", where, err)
				}
				n, err = w.touched(tx, insert,
					visStranger, "other "+where, "ref_other_"+role+"_"+visibility, false, visibility, teamIDs)
				if err == nil || !strings.Contains(err.Error(), "row-level security") {
					t.Fatalf("%s owned by the caller only: %d, %v", where, n, err)
				}
			})
		}
		where := role + " insert house"
		w.as(w.people[role], w.allow(role), "{}", func(tx pgx.Tx) {
			n, err := w.touched(tx, insert,
				"", "house "+role, "ref_house_"+role, true, "everyone", "{}")
			if err == nil || !strings.Contains(err.Error(), "row-level security") {
				t.Fatalf("%s: %d, %v", where, n, err)
			}
		})
	}
}

func (w *visWorld) countOwner(sql string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func allowed(n int64, err error) bool { return err == nil && n == 1 }

func nullUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func teamArg(account visAccount) string {
	if account.visibility == "team" {
		return "{" + visTeam + "}"
	}
	return "{}"
}

func TestAMemberCannotShareAndAnAdminCan(t *testing.T) {
	w := newVisWorld(t)
	insert := `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, name, adapter, credential_ref, plan_kind, house, visibility, team_ids)
		VALUES ($1::uuid, $2, 'fake', $3, 'local', false, $4, $5::uuid[])`
	w.as(visMember, "task:view", "{}", func(tx pgx.Tx) {
		if _, err := w.touched(tx, insert, visMember, "shared by a member", "ref_member_share", "everyone", "{}"); err == nil {
			t.Fatal("a member shared an account")
		}
		if _, err := w.touched(tx, insert, visMember, "team by a member", "ref_member_team", "team", "{"+visTeam+"}"); err == nil {
			t.Fatal("a member shared an account with a team")
		}
		n, err := w.touched(tx, insert, visMember, "private by a member", "ref_member_private", "private", "{}")
		if err != nil || n != 1 {
			t.Fatalf("a member's own account: %d, %v", n, err)
		}
	})
	for _, role := range []string{"admin", "custom"} {
		w.as(w.people[role], "settings:manage", "{}", func(tx pgx.Tx) {
			n, err := w.touched(tx, insert, w.people[role], "shared by "+role, "ref_share_"+role, "everyone", "{}")
			if err != nil || n != 1 {
				t.Fatalf("%s share: %d, %v", role, n, err)
			}
			n, err = w.touched(tx, insert, w.people[role], "team by "+role, "ref_team_"+role, "team", "{"+visTeam+"}")
			if err != nil || n != 1 {
				t.Fatalf("%s team share: %d, %v", role, n, err)
			}
		})
	}
}

func TestOnlyTheOwnerTakesASharedAccountPrivate(t *testing.T) {
	w := newVisWorld(t)
	stmt := `UPDATE gateway.provider_accounts SET visibility = 'private', team_ids = '{}' WHERE id = $1::uuid`
	team := w.shared["team"]
	for _, role := range []string{"owner", "admin", "member", "viewer", "custom"} {
		w.as(w.people[role], w.allow(role), "{}", func(tx pgx.Tx) {
			n, err := w.touched(tx, stmt, team.id)
			got := err == nil && n == 1
			if got != (role == "admin") {
				t.Errorf("%s took the account private: %d, %v", role, n, err)
			}
		})
	}
}

func TestAnAdminOffTheTeamIsNotRoutedThroughIt(t *testing.T) {
	w := newVisWorld(t)
	ctx := context.Background()
	store := postgres.Store{Pool: w.owner}
	team := w.shared["team"]
	// The admin owns the team account and is not on its team.
	if _, ok, err := store.PinProvider(ctx, visAdmin, nil, "routed", "m"); err != nil || ok {
		t.Fatalf("admin off the team was routed: ok=%v %v", ok, err)
	}
	got, ok, err := store.PinProvider(ctx, visAdmin, []string{visTeam}, "routed", "m")
	if err != nil || !ok || got.ID != team.id {
		t.Fatalf("admin on the team: %+v ok=%v %v", got, ok, err)
	}
	if _, ok, err := store.PinProvider(ctx, visMember, nil, "routed", "m"); err != nil || ok {
		t.Fatalf("member off the team was routed: ok=%v %v", ok, err)
	}
	got, ok, err = store.PinProvider(ctx, visMember, []string{visTeam}, "routed", "m")
	if err != nil || !ok || got.ID != team.id {
		t.Fatalf("member on the team: %+v ok=%v %v", got, ok, err)
	}
}

func TestSharedAccountsDoNotCountTowardThePersonalCap(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, name, adapter, credential_ref, plan_kind, house, visibility)
		SELECT $1::uuid, 'p' || i, 'fake', 'cap_p_' || i, 'local', false, 'private'
		FROM generate_series(1, $2) AS i`, visAdmin, domain.MaxOwnAccounts-1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, name, adapter, credential_ref, plan_kind, house, visibility)
		SELECT $1::uuid, 's' || i, 'fake', 'cap_s_' || i, 'local', false, 'everyone'
		FROM generate_series(1, 5) AS i`, visAdmin); err != nil {
		t.Fatal(err)
	}
	providers := postgres.Providers{Pool: pgtest.App(t, pool)}
	personal := domain.Provider{Provider: "custom", Name: "the twentieth", Adapter: "fake", CredentialRef: "cap_last", PlanKind: "local", Own: true}
	if _, err := providers.Create(ctx, app.Caller{User: visAdmin}, personal, nil); err != nil {
		t.Fatal(err)
	}
	personal.Name, personal.CredentialRef = "one too many", "cap_over"
	if _, err := providers.Create(ctx, app.Caller{User: visAdmin}, personal, nil); !errors.Is(err, app.ErrLimit) {
		t.Fatalf("the personal cap: %v", err)
	}
	shared := domain.Provider{
		Provider: "custom", Name: "still shared", Adapter: "fake", CredentialRef: "cap_shared",
		PlanKind: "local", Share: domain.Share{Visibility: domain.VisibilityEveryone},
	}
	if _, err := providers.Create(ctx, app.Caller{User: visAdmin, Allow: domain.SharePermission}, shared, nil); err != nil {
		t.Fatalf("a shared account was counted: %v", err)
	}
	var private, sharedN int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE visibility = 'private'),
			count(*) FILTER (WHERE visibility <> 'private')
		FROM gateway.provider_accounts WHERE owner_user_id = $1::uuid`, visAdmin).Scan(&private, &sharedN); err != nil {
		t.Fatal(err)
	}
	if private != domain.MaxOwnAccounts || sharedN != 6 {
		t.Fatalf("private = %d shared = %d, want %d and 6", private, sharedN, domain.MaxOwnAccounts)
	}
}

func TestFeature_UPM277_TurningSharedPrivateHitsThePersonalCap(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, name, adapter, credential_ref, plan_kind, house, visibility)
		SELECT $1::uuid, 'p' || i, 'fake', 'turn_p_' || i, 'local', false, 'private'
		FROM generate_series(1, $2) AS i`, visAdmin, domain.MaxOwnAccounts); err != nil {
		t.Fatal(err)
	}
	var id, ref string
	if err := pool.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ($1::uuid, 'shared one', 'fake', 'turn_shared', 'local', false, 'everyone')
		RETURNING id::text, credential_ref`, visAdmin).Scan(&id, &ref); err != nil {
		t.Fatal(err)
	}
	providers := postgres.Providers{Pool: pgtest.App(t, pool)}
	who := app.Caller{User: visAdmin, Allow: domain.SharePermission}
	turning := domain.Provider{
		ID: id, Name: "shared one", Adapter: "fake", CredentialRef: ref, PlanKind: "local",
		Own: true, Share: domain.Share{Visibility: domain.VisibilityPrivate},
	}
	if _, err := providers.Update(ctx, who, turning, nil, false); !errors.Is(err, app.ErrLimit) {
		t.Fatalf("over the cap: %v", err)
	}
	var visibility string
	if err := pool.QueryRow(ctx, `SELECT visibility FROM gateway.provider_accounts WHERE id = $1::uuid`, id).Scan(&visibility); err != nil || visibility != "everyone" {
		t.Fatalf("visibility = %q, %v", visibility, err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM gateway.provider_accounts
		WHERE owner_user_id = $1::uuid AND visibility = 'private' AND name = 'p1'`, visAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := providers.Update(ctx, who, turning, nil, false); err != nil {
		t.Fatalf("under the cap: %v", err)
	}
}

func TestATeamChangeTakesEffectOnTheNextRequest(t *testing.T) {
	w := newVisWorld(t)
	ctx := context.Background()
	w.exec(`INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, visTeam, visMember)
	see := func() int {
		t.Helper()
		tx, err := w.app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `
			SELECT set_config('upmo.user_id', $1, true),
				set_config('upmo.allow', '', true),
				set_config('upmo.teams', COALESCE((
					SELECT array_agg(team_id ORDER BY team_id)::text
					FROM identity.team_members WHERE user_id = $1::uuid
				), '{}'), true)`, visMember); err != nil {
			t.Fatal(err)
		}
		return w.count(tx, `SELECT count(*) FROM gateway.provider_accounts WHERE id = $1::uuid`, w.shared["team"].id)
	}
	if see() != 1 {
		t.Fatal("a member of the team does not see its account")
	}
	w.exec(`DELETE FROM identity.team_members WHERE user_id = $1::uuid`, visMember)
	if see() != 0 {
		t.Fatal("leaving the team still showed the account on the next request")
	}
}

func TestProviderAccountVisibilityUpDownUp(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	provider, db := gooseGateway(t, pool)
	defer func() { _ = db.Close() }()
	if _, err := provider.UpTo(ctx, 5); err != nil {
		t.Fatalf("up to 5: %v", err)
	}
	// No joined owner: a shared account cannot be given one.
	if _, err := pool.Exec(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house)
		VALUES ('orphan', 'fake', 'orphan', 'local', false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err == nil || !strings.Contains(err.Error(), "provider accounts need a joined owner") {
		t.Fatalf("up with no owner: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM gateway.provider_accounts WHERE name = 'orphan'`); err != nil {
		t.Fatal(err)
	}

	early, later, disabled, absent := insertOwners(t, pool)
	personal := insertOwned(t, pool, early)
	var teamA, teamB, teamAccount, workspace, house string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Alpha') RETURNING id::text`).Scan(&teamA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Beta') RETURNING id::text`).Scan(&teamB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house)
		VALUES ('for a team', 'fake', 'team-ref', 'local', false) RETURNING id::text`).Scan(&teamAccount); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{teamB, teamA} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO gateway.provider_account_teams (account_id, team_id)
			VALUES ($1::uuid, $2::uuid)`, teamAccount, team); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house)
		VALUES ('for everyone', 'fake', 'everyone-ref', 'local', false) RETURNING id::text`).Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house)
		VALUES ('the house', 'fake', 'house-ref', 'local', true) RETURNING id::text`).Scan(&house); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	assertBackfill(t, pool, early, later, disabled, absent, personal, teamAccount, workspace, house, []string{teamA, teamB})
	if n := countGatewayFns(t, pool, "may_share", "my_teams", "team_shared"); n != 0 {
		t.Fatalf("%d old sharing functions remain", n)
	}
	if n := countRelation(t, pool, "provider_account_teams"); n != 0 {
		t.Fatal("provider_account_teams remains")
	}

	if _, err := provider.DownTo(ctx, 5); err != nil {
		t.Fatalf("down to 5: %v", err)
	}
	assertDown(t, pool, personal, teamAccount, workspace, house, []string{teamA, teamB})
	if n := countGatewayFns(t, pool, "may_share", "my_teams", "team_shared"); n != 3 {
		t.Fatalf("sharing functions after down = %d, want 3", n)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	assertBackfill(t, pool, early, later, disabled, absent, personal, teamAccount, workspace, house, []string{teamA, teamB})
	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version_id) FROM gateway.goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version after re-up = %d, %v", version, err)
	}
}

func insertOwners(t *testing.T, pool *pgxpool.Pool) (early, later, disabled, absent string) {
	t.Helper()
	ctx := context.Background()
	insert := `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, $1, (SELECT id FROM identity.roles WHERE key = 'owner'), $2)
		RETURNING id::text`
	// The earliest enabled owner is the one a shared account is given. A
	// disabled owner who joined sooner, and one who has not joined, are not.
	if err := pool.QueryRow(ctx, insert, "early@example.com", "2020-01-02").Scan(&early); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at, disabled_at)
		VALUES ('disabled@example.com', 'disabled', (SELECT id FROM identity.roles WHERE key = 'owner'), '2020-01-01', '2020-02-01')
		RETURNING id::text`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, insert, "later@example.com", "2020-03-01").Scan(&later); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id)
		VALUES ('absent@example.com', 'absent', (SELECT id FROM identity.roles WHERE key = 'owner'))
		RETURNING id::text`).Scan(&absent); err != nil {
		t.Fatal(err)
	}
	return early, later, disabled, absent
}

func insertOwned(t *testing.T, pool *pgxpool.Pool, owner string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO gateway.provider_accounts (owner_user_id, name, adapter, credential_ref, plan_kind, house)
		VALUES ($1::uuid, 'personal', 'fake', 'personal-ref', 'local', false)
		RETURNING id::text`, owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertBackfill(t *testing.T, pool *pgxpool.Pool, early, later, disabled, absent, personal, teamAccount, workspace, house string, teams []string) {
	t.Helper()
	ctx := context.Background()
	type row struct {
		visibility, owner string
		house             bool
		teamIDs           []string
	}
	read := func(id string) row {
		t.Helper()
		var got row
		if err := pool.QueryRow(ctx, `
			SELECT visibility, COALESCE(owner_user_id::text, ''), house, team_ids::text[]
			FROM gateway.provider_accounts WHERE id = $1::uuid`, id).Scan(&got.visibility, &got.owner, &got.house, &got.teamIDs); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(personal); got.visibility != "private" || got.owner != early || got.house || len(got.teamIDs) != 0 {
		t.Fatalf("personal = %+v, want private owned by the early owner", got)
	}
	got := read(teamAccount)
	slices.Sort(teams)
	slices.Sort(got.teamIDs)
	if got.visibility != "team" || got.owner != early || !slices.Equal(got.teamIDs, teams) {
		t.Fatalf("team = %+v, want team owned by %s teams %v", got, early, teams)
	}
	if got := read(workspace); got.visibility != "everyone" || got.owner != early || got.house || len(got.teamIDs) != 0 {
		t.Fatalf("workspace = %+v, want everyone owned by the early owner", got)
	}
	if got := read(house); got.visibility != "everyone" || got.owner != "" || !got.house || len(got.teamIDs) != 0 {
		t.Fatalf("house = %+v", got)
	}
	for _, other := range []string{later, disabled, absent} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM gateway.provider_accounts WHERE owner_user_id = $1::uuid`, other).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("owner %s was given %d accounts", other, n)
		}
	}
	var ledger int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'gateway' AND table_name = 'ledger' AND column_name = 'owner_team_id'`).Scan(&ledger); err != nil || ledger != 1 {
		t.Fatalf("ledger.owner_team_id = %d, %v", ledger, err)
	}
}

func assertDown(t *testing.T, pool *pgxpool.Pool, personal, teamAccount, workspace, house string, teams []string) {
	t.Helper()
	ctx := context.Background()
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'gateway' AND table_name = 'provider_accounts'
		  AND column_name IN ('visibility', 'team_ids')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("visibility columns after down = %d, %v", columns, err)
	}
	ownerOf := func(id string) (owner string, house bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT COALESCE(owner_user_id::text, ''), house
			FROM gateway.provider_accounts WHERE id = $1::uuid`, id).Scan(&owner, &house); err != nil {
			t.Fatal(err)
		}
		return owner, house
	}
	if owner, house := ownerOf(personal); owner == "" || house {
		t.Fatalf("personal owner after down = %q house %v", owner, house)
	}
	for _, id := range []string{teamAccount, workspace} {
		if owner, house := ownerOf(id); owner != "" || house {
			t.Fatalf("shared %s owner after down = %q house %v", id, owner, house)
		}
	}
	if owner, houseRow := ownerOf(house); owner != "" || !houseRow {
		t.Fatalf("house after down = %q house %v", owner, houseRow)
	}
	var got []string
	rows, err := pool.Query(ctx, `
		SELECT team_id::text FROM gateway.provider_account_teams
		WHERE account_id = $1::uuid ORDER BY team_id`, teamAccount)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	rows.Close()
	slices.Sort(teams)
	if err := rows.Err(); err != nil || !slices.Equal(got, teams) {
		t.Fatalf("team rows after down = %v, %v, want %v", got, err, teams)
	}
	var index int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = 'gateway' AND indexname = 'provider_accounts_owner_name'`).Scan(&index); err != nil || index != 1 {
		t.Fatalf("owner name index = %d, %v", index, err)
	}
}

func gooseGateway(t *testing.T, pool *pgxpool.Pool) (*goose.Provider, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	// Identity first, as brain does: its schema, the upmo_app role, and the
	// rules later gateway migrations call. Goose writes its version table into
	// the gateway schema, which migrate.Up creates and a raw provider does not.
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+Schema); err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	provider, err := goose.NewProvider(goose.DialectPostgres, db, Migrations(), goose.WithTableName(Schema+".goose_db_version"))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return provider, db
}

func countGatewayFns(t *testing.T, pool *pgxpool.Pool, names ...string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_proc AS proc
		JOIN pg_namespace AS nsp ON nsp.oid = proc.pronamespace
		WHERE nsp.nspname = 'gateway' AND proc.proname = ANY($1::text[])`, names).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countRelation(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_class AS tab
		JOIN pg_namespace AS nsp ON nsp.oid = tab.relnamespace
		WHERE nsp.nspname = 'gateway' AND tab.relname = $1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
