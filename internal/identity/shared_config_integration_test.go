//go:build integration

package identity_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// Fixed ids for the pure rules: they read their arguments, not the tables, so
// no rows in identity.users are needed to decide access.
var (
	ruleUser  = "00000000-0000-7000-8000-000000000001"
	ruleOther = "00000000-0000-7000-8000-000000000002"
	ruleTeamA = "00000000-0000-7000-8000-000000000003"
	ruleTeamB = "00000000-0000-7000-8000-000000000004"
)

func TestCanUseDecidesAccess(t *testing.T) {
	owner, ctx := upOwner(t)
	app := pgtest.App(t, owner)
	cases := []struct {
		name                 string
		me, teams, itemOwner string
		visibility           string
		shared               []string
		want                 bool
	}{
		{"the owner uses their private item", ruleUser, "{}", ruleUser, "private", nil, true},
		{"another person's private item stays closed", ruleOther, "{" + ruleTeamA + "}", ruleUser, "private", nil, false},
		{"an everyone item is usable by anyone", ruleOther, "{}", ruleUser, "everyone", nil, true},
		{"a team item reaches its team", ruleOther, "{" + ruleTeamA + "}", ruleUser, "team", []string{ruleTeamA}, true},
		{"a team item hides from another team", ruleOther, "{" + ruleTeamB + "}", ruleUser, "team", []string{ruleTeamA}, false},
		{"a team item hides from people on no team", ruleOther, "{}", ruleUser, "team", []string{ruleTeamA}, false},
		{"the owner of a team item off all its teams gets nothing", ruleUser, "{}", ruleUser, "team", []string{ruleTeamA}, false},
		{"a team item with no teams shares with nobody", ruleOther, "{" + ruleTeamA + "}", ruleUser, "team", []string{}, false},
		{"an unknown visibility grants the owner nothing", ruleUser, "{}", ruleUser, "workspace", nil, false},
		{"an unknown visibility grants a team member nothing", ruleOther, "{" + ruleTeamA + "}", ruleUser, "workspace", []string{ruleTeamA}, false},
		{"no user uses nothing", "", "{}", ruleUser, "everyone", nil, false},
		{"no user uses nothing on a private item", "", "{" + ruleTeamA + "}", ruleUser, "private", []string{ruleTeamA}, false},
		{"a null owner's private item has no owner to use it", ruleUser, "{}", "", "private", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Empty means unset, mirroring the settings NULLIF that feeds the
			// rule in production.
			got := decide(t, app, ctx, nil, `
				SELECT identity.can_use(NULLIF($1, '')::uuid, $2::uuid[], NULLIF($3, '')::uuid, $4, $5::uuid[])`,
				tc.me, tc.teams, tc.itemOwner, tc.visibility, tc.shared)
			if got != tc.want {
				t.Fatalf("can_use = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMayUseReadsTheRequestSettings(t *testing.T) {
	owner, ctx := upOwner(t)
	app := pgtest.App(t, owner)
	cases := []struct {
		name                  string
		settings              map[string]string
		itemOwner, visibility string
		shared                []string
		want                  bool
	}{
		{"the owner uses their private item", map[string]string{"upmo.user_id": ruleUser}, ruleUser, "private", nil, true},
		{"another person's private item stays closed", map[string]string{"upmo.user_id": ruleOther, "upmo.teams": "{" + ruleTeamA + "}"}, ruleUser, "private", nil, false},
		{"a team item reaches its team", map[string]string{"upmo.user_id": ruleOther, "upmo.teams": "{" + ruleTeamA + "}"}, ruleUser, "team", []string{ruleTeamA}, true},
		{"the owner of a team item off all its teams gets nothing", map[string]string{"upmo.user_id": ruleUser}, ruleUser, "team", []string{ruleTeamA}, false},
		{"an everyone item is usable by anyone", map[string]string{"upmo.user_id": ruleOther}, ruleUser, "everyone", nil, true},
		{"an unknown visibility grants nothing", map[string]string{"upmo.user_id": ruleUser}, ruleUser, "workspace", nil, false},
		{"a team item with no teams shares with nobody", map[string]string{"upmo.user_id": ruleOther, "upmo.teams": "{" + ruleTeamA + "}"}, ruleUser, "team", []string{}, false},
		{"an unset user grants nothing", nil, ruleUser, "everyone", nil, false},
		{"an empty user grants nothing", map[string]string{"upmo.user_id": ""}, ruleUser, "private", nil, false},
		{"an unset teams setting is no teams", map[string]string{"upmo.user_id": ruleOther}, ruleUser, "team", []string{ruleTeamA}, false},
		{"empty settings grant nothing", map[string]string{"upmo.user_id": "", "upmo.teams": ""}, ruleUser, "everyone", nil, false},
		{"a null owner's private item has no owner to use it", map[string]string{"upmo.user_id": ruleUser}, "", "private", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(t, app, ctx, tc.settings, `
				SELECT identity.may_use(NULLIF($1, '')::uuid, $2, $3::uuid[])`,
				tc.itemOwner, tc.visibility, tc.shared)
			if got != tc.want {
				t.Fatalf("may_use = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMayManageReadsTheRequestSettings(t *testing.T) {
	owner, ctx := upOwner(t)
	app := pgtest.App(t, owner)
	cases := []struct {
		name                  string
		settings              map[string]string
		itemOwner, visibility string
		want                  bool
	}{
		{"the owner manages their private item", map[string]string{"upmo.user_id": ruleUser}, ruleUser, "private", true},
		{"another person's private item stays closed even with settings:manage", map[string]string{"upmo.user_id": ruleOther, "upmo.allow": "settings:manage"}, ruleUser, "private", false},
		{"settings:manage opens team items", map[string]string{"upmo.user_id": ruleOther, "upmo.allow": "settings:manage"}, ruleUser, "team", true},
		{"settings:manage among other permissions opens team items", map[string]string{"upmo.user_id": ruleOther, "upmo.allow": "task:view,settings:manage"}, ruleUser, "team", true},
		{"other permissions do not open team items", map[string]string{"upmo.user_id": ruleOther, "upmo.allow": "task:view"}, ruleUser, "team", false},
		{"an unset allow opens nothing", map[string]string{"upmo.user_id": ruleOther}, ruleUser, "team", false},
		{"settings:manage opens everyone items", map[string]string{"upmo.user_id": ruleOther, "upmo.allow": "settings:manage"}, ruleUser, "everyone", true},
		{"an empty allow opens no everyone items", map[string]string{"upmo.user_id": ruleOther, "upmo.allow": ""}, ruleUser, "everyone", false},
		{"no user manages nothing private", map[string]string{"upmo.allow": "settings:manage"}, ruleUser, "private", false},
		{"settings:manage holds without a user", map[string]string{"upmo.allow": "settings:manage"}, ruleUser, "team", true},
		{"no settings at all manage nothing", nil, ruleUser, "private", false},
		{"an unknown visibility grants nothing", map[string]string{"upmo.user_id": ruleOther, "upmo.allow": "settings:manage"}, ruleUser, "workspace", false},
		{"a null owner's private item has no owner to manage it", map[string]string{"upmo.user_id": ruleUser}, "", "private", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(t, app, ctx, tc.settings, `
				SELECT identity.may_manage(NULLIF($1, '')::uuid, $2)`,
				tc.itemOwner, tc.visibility)
			if got != tc.want {
				t.Fatalf("may_manage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTeamsOfListsThePersonsTeams(t *testing.T) {
	owner, ctx := upOwner(t)
	person := seedOwner(t, owner, ctx, "ada@example.com")
	other := insertUser(t, owner, ctx, "noor@example.com")
	teams := makeTeams(t, owner, ctx)
	joinTeam(t, owner, ctx, teams[0], person)
	joinTeam(t, owner, ctx, teams[2], person)
	var got []string
	if err := owner.QueryRow(ctx, `SELECT identity.teams_of($1::uuid)::text[]`, person).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := []string{teams[0], teams[2]}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("teams_of = %v, want %v", got, want)
	}
	if err := owner.QueryRow(ctx, `SELECT identity.teams_of($1::uuid)::text[]`, other).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("teams_of = %v, want none", got)
	}
}

func TestAppRoleRunsTheRulesButNotTeamsOf(t *testing.T) {
	owner, ctx := upOwner(t)
	app := pgtest.App(t, owner)
	if _, err := app.Exec(ctx, `SELECT identity.teams_of($1::uuid)`, ruleUser); err == nil {
		t.Fatal("upmo_app resolved teams")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("teams_of failed for another reason: %v", err)
	}
	if _, err := app.Exec(ctx, `SELECT identity.can_use($1::uuid, '{}'::uuid[], $1::uuid, 'everyone', '{}'::uuid[])`, ruleUser); err != nil {
		t.Fatal(err)
	}
	use := decide(t, app, ctx, map[string]string{"upmo.user_id": ruleUser}, `
		SELECT identity.may_use($1::uuid, 'private', '{}'::uuid[])`, ruleUser)
	if !use {
		t.Fatal("may_use denied the owner")
	}
	manage := decide(t, app, ctx, map[string]string{"upmo.user_id": ruleUser}, `
		SELECT identity.may_manage($1::uuid, 'private')`, ruleUser)
	if !manage {
		t.Fatal("may_manage denied the owner")
	}
}

func TestSharedConfigUpDownUp(t *testing.T) {
	owner, ctx := upOwner(t)
	db := stdlib.OpenDB(*owner.Config().ConnConfig)
	defer func() { _ = db.Close() }()
	// Wired like internal/platform/migrate.open, so the down runs the same
	// statements goose would run in production.
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		identity.Migrations(),
		goose.WithTableName(identity.Schema+".goose_db_version"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Down version 2 only: the schema and version 1 objects survive.
	if _, err := provider.DownTo(ctx, 1); err != nil {
		t.Fatalf("down to 1: %v", err)
	}
	if fns := sharedConfigFunctions(t, owner, ctx); fns != 0 {
		t.Fatalf("%d shared config functions remain after down", fns)
	}
	var version int
	if err := owner.QueryRow(ctx, `SELECT max(version_id) FROM identity.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version after down = %d, want 1", version)
	}
	var tables int
	if err := owner.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_class AS tab
		JOIN pg_namespace AS nsp ON nsp.oid = tab.relnamespace
		WHERE nsp.nspname = 'identity'
		  AND tab.relkind = 'r'
		  AND tab.relname IN ('roles', 'users', 'teams', 'team_members')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 4 {
		t.Fatalf("%d version 1 tables remain after down, want 4", tables)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	if fns := sharedConfigFunctions(t, owner, ctx); fns != 4 {
		t.Fatalf("%d shared config functions after re-up, want 4", fns)
	}
	if err := owner.QueryRow(ctx, `SELECT max(version_id) FROM identity.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("version after re-up = %d, want 3", version)
	}
	app := pgtest.App(t, owner)
	var ok bool
	if err := app.QueryRow(ctx, `SELECT identity.can_use($1::uuid, '{}'::uuid[], $1::uuid, 'everyone', '{}'::uuid[])`, ruleUser).Scan(&ok); err != nil || !ok {
		t.Fatalf("can_use after re-up = %v, %v", ok, err)
	}
	if _, err := app.Exec(ctx, `SELECT identity.teams_of($1::uuid)`, ruleUser); err == nil {
		t.Fatal("upmo_app resolved teams after re-up")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("teams_of failed for another reason after re-up: %v", err)
	}
}

// sharedConfigFunctions counts the migration 2 functions in the identity schema.
func sharedConfigFunctions(t *testing.T, pool *pgxpool.Pool, ctx context.Context) int {
	t.Helper()
	var fns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_proc AS proc
		JOIN pg_namespace AS nsp ON nsp.oid = proc.pronamespace
		WHERE nsp.nspname = 'identity'
		  AND proc.proname IN ('can_use', 'may_use', 'may_manage', 'teams_of')`).Scan(&fns); err != nil {
		t.Fatal(err)
	}
	return fns
}

// decide runs one rule call in a transaction that first writes the request
// settings, mirroring how the app writes them around every transaction. A nil
// settings map leaves them unset.
func decide(t *testing.T, pool *pgxpool.Pool, ctx context.Context, settings map[string]string, query string, args ...any) bool {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for name, value := range settings {
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, name, value); err != nil {
			t.Fatal(err)
		}
	}
	var got bool
	if err := tx.QueryRow(ctx, query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}
