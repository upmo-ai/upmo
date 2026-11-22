//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// shareKeys maps each credential ref to the key it resolves to, so a call
// shows whose key it spent.
var shareKeys = map[string]string{
	"ref_ws":   "key-ws",
	"ref_fin":  "key-fin",
	"ref_ops":  "key-ops",
	"ref_both": "key-both",
	"ref_mine": "key-mine",
}

// shareWorld is a gateway database with a workspace account, an account for
// each of two teams, one for both, and a personal one, all on one chat route,
// and four people: on Finance, on Ops, on both, and on neither.
type shareWorld struct {
	pool    *pgxpool.Pool
	disp    app.Dispatch
	store   postgres.Store
	adapter *pinAdapter

	finance, ops, both, outsider string
	financeTeam, opsTeam         string

	// The accounts, by the name they were given.
	workspace, financeAccount, opsAccount, bothAccount, bothOwn string
}

func newShareWorld(t *testing.T, fail map[string]domain.FailureClass) shareWorld {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	w := shareWorld{pool: pool, store: postgres.Store{Pool: pool}, adapter: &pinAdapter{fail: fail}}

	addPerson := func(name, role string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1, sha256(convert_to($1, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3), now())
			RETURNING id::text`, name+"@example.com", name, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	addTeam := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	join := func(teamID, personID string) {
		if _, err := pool.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, teamID, personID); err != nil {
			t.Fatal(err)
		}
	}
	founder := addPerson("founder", "owner")
	w.finance = addPerson("finance", "member")
	w.ops = addPerson("ops", "member")
	w.both = addPerson("both", "member")
	w.outsider = addPerson("outsider", "member")
	w.financeTeam, w.opsTeam = addTeam("Finance"), addTeam("Ops")
	join(w.financeTeam, w.finance)
	join(w.opsTeam, w.ops)
	join(w.financeTeam, w.both)
	join(w.opsTeam, w.both)

	// addAccount makes an account that lists its route model and "common",
	// owned by owner (or by none) and shared with teams (or with none).
	addAccount := func(name, owner, ref, model string, teams ...string) string {
		ownerID := owner
		visibility := "private"
		if owner == "" {
			ownerID = founder
			visibility = "everyone"
			if len(teams) > 0 {
				visibility = "team"
			}
		}
		if teams == nil {
			teams = []string{}
		}
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO gateway.provider_accounts
				(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, visibility, team_ids)
			VALUES ($1::uuid, 'acme', $2, 'fake', $3, 'metered', false, '', $4, $5::uuid[])
			RETURNING id::text`, ownerID, name, ref, visibility, teams).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for i, m := range []string{model, "common"} {
			if _, err := pool.Exec(ctx, `INSERT INTO gateway.provider_models (account_id, model, position) VALUES ($1::uuid, $2, $3)`, id, m, i+1); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	w.workspace = addAccount("a-ws", "", "ref_ws", "m-ws")
	w.financeAccount = addAccount("b-fin", "", "ref_fin", "m-fin", w.financeTeam)
	w.opsAccount = addAccount("c-ops", "", "ref_ops", "m-ops", w.opsTeam)
	w.bothAccount = addAccount("d-both", "", "ref_both", "m-both", w.financeTeam, w.opsTeam)
	w.bothOwn = addAccount("e-mine", w.both, "ref_mine", "m-mine")

	var route string
	if err := pool.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ('r', 'chat', true) RETURNING id::text`).Scan(&route); err != nil {
		t.Fatal(err)
	}
	for i, l := range []struct{ account, model string }{
		{w.workspace, "m-ws"}, {w.financeAccount, "m-fin"}, {w.opsAccount, "m-ops"}, {w.bothAccount, "m-both"}, {w.bothOwn, "m-mine"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
			VALUES ($1::uuid, $2, $3::uuid, $4, 0)`, route, i+1, l.account, l.model); err != nil {
			t.Fatal(err)
		}
	}
	w.disp = app.Dispatch{
		Routes:  w.store,
		Pins:    w.store,
		Teams:   w.store,
		Ledger:  w.store,
		Secrets: pinSecrets(shareKeys),
		Stream:  map[string]app.StreamChatter{domain.AdapterFake: w.adapter},
	}
	return w
}

func (w shareWorld) stream(t *testing.T, user string, pin domain.Pin) (*pinSink, error) {
	t.Helper()
	sink := &pinSink{}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	err := w.disp.ChatStreamPinned(context.Background(), "r", "", domain.Principal{User: user, Source: "system"}, in, pin, 0, sink)
	return sink, err
}

// ledgerTeams is the team each attempt of a model was booked against, empty
// for none.
func (w shareWorld) ledgerTeams(t *testing.T) map[string]string {
	t.Helper()
	rows, err := w.pool.Query(context.Background(), `SELECT model, COALESCE(owner_team_id::text, '') FROM gateway.ledger`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var model, team string
		if err := rows.Scan(&model, &team); err != nil {
			t.Fatal(err)
		}
		out[model] = team
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (w shareWorld) lowestTeam() string {
	if w.opsTeam < w.financeTeam {
		return w.opsTeam
	}
	return w.financeTeam
}

func modelsOf(links []domain.Link) string {
	var out []string
	for _, l := range links {
		out = append(out, l.Model)
	}
	return strings.Join(out, ",")
}

func TestLinksForUserOverTheDatabaseAreOwnThenTeamThenWorkspace(t *testing.T) {
	w := newShareWorld(t, nil)
	ctx := context.Background()
	route, err := w.store.ByName(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		user      string
		wantTeams int
		want      string
	}{
		// The account both teams share is one link, and the personal one is first.
		"both":     {w.both, 2, "m-mine,m-fin,m-ops,m-both,m-ws"},
		"finance":  {w.finance, 1, "m-fin,m-both,m-ws"},
		"ops":      {w.ops, 1, "m-ops,m-both,m-ws"},
		"outsider": {w.outsider, 0, "m-ws"},
	} {
		teams, err := w.store.TeamsOf(ctx, tc.user)
		if err != nil || len(teams) != tc.wantTeams {
			t.Fatalf("%s: teams = %v, %v", name, teams, err)
		}
		links, err := route.LinksForUser(tc.user, teams)
		if err != nil || modelsOf(links) != tc.want {
			t.Errorf("%s: chain = %s, %v, want %s", name, modelsOf(links), err, tc.want)
		}
	}
	// A person that is no one on the install is on no team.
	if teams, err := w.store.TeamsOf(ctx, "not-a-uuid"); err != nil || len(teams) != 0 {
		t.Errorf("teams of a stranger = %v, %v", teams, err)
	}
}

func TestStreamSpendsTheTeamsKeyAndTheLedgerRemembersWhichTeam(t *testing.T) {
	// Everything before the workspace link fails, so every link is tried and
	// booked.
	fail := map[string]domain.FailureClass{
		"m-mine": domain.ClassTransient, "m-fin": domain.ClassTransient,
		"m-ops": domain.ClassTransient, "m-both": domain.ClassTransient,
	}
	w := newShareWorld(t, fail)
	sink, err := w.stream(t, w.both, domain.Pin{})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.keysSpent(); got != "key-mine,key-fin,key-ops,key-both,key-ws" {
		t.Fatalf("keys spent = %s", got)
	}
	if sink.done == nil || sink.done.Model != "m-ws" {
		t.Fatalf("done = %+v fail = %+v", sink.done, sink.fail)
	}
	// The team an attempt is booked against is the one the account was
	// reached through. The account both teams share is booked against the
	// lowest team id of the two.
	want := map[string]string{
		"m-mine": "", "m-fin": w.financeTeam, "m-ops": w.opsTeam, "m-both": w.lowestTeam(), "m-ws": "",
	}
	got := w.ledgerTeams(t)
	if len(got) != len(want) {
		t.Fatalf("ledger = %v", got)
	}
	for model, team := range want {
		if got[model] != team {
			t.Errorf("%s was booked against team %q, want %q", model, got[model], team)
		}
	}
}

func TestAMemberOnOneTeamSpendsOnlyThatTeamsKey(t *testing.T) {
	fail := map[string]domain.FailureClass{"m-fin": domain.ClassTransient, "m-both": domain.ClassTransient}
	w := newShareWorld(t, fail)
	if _, err := w.stream(t, w.finance, domain.Pin{}); err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.keysSpent(); got != "key-fin,key-both,key-ws" {
		t.Fatalf("finance spent %s", got)
	}
	// Both are booked against Finance, the only team the person is on.
	got := w.ledgerTeams(t)
	if got["m-fin"] != w.financeTeam || got["m-both"] != w.financeTeam || got["m-ws"] != "" {
		t.Fatalf("ledger = %v", got)
	}

	// Someone on no team gets the workspace account and nothing of any team.
	w = newShareWorld(t, nil)
	if _, err := w.stream(t, w.outsider, domain.Pin{}); err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.keysSpent(); got != "key-ws" {
		t.Fatalf("outsider spent %s", got)
	}
	if got := w.ledgerTeams(t); got["m-ws"] != "" || len(got) != 1 {
		t.Fatalf("ledger = %v", got)
	}
}

func TestPinToATeamAccountWorksForItsMembersAndNoOthers(t *testing.T) {
	w := newShareWorld(t, nil)
	sink, err := w.stream(t, w.finance, domain.Pin{AccountID: w.financeAccount, Model: "m-fin"})
	if err != nil {
		t.Fatal(err)
	}
	if w.adapter.models() != "m-fin" || w.adapter.keysSpent() != "key-fin" || sink.done == nil {
		t.Fatalf("models = %s keys = %s done = %+v", w.adapter.models(), w.adapter.keysSpent(), sink.done)
	}
	// The attempt is booked against the team the pin was reached through.
	if got := w.ledgerTeams(t); got["m-fin"] != w.financeTeam {
		t.Fatalf("ledger = %v", got)
	}

	// A member of the other team, and a member of none, are refused before any
	// call, and the refusal names no account.
	for name, pick := range map[string]func(shareWorld) string{
		"ops":      func(w shareWorld) string { return w.ops },
		"outsider": func(w shareWorld) string { return w.outsider },
	} {
		w := newShareWorld(t, nil)
		_, err := w.stream(t, pick(w), domain.Pin{AccountID: w.financeAccount, Model: "m-fin"})
		if !errors.Is(err, domain.ErrPinAccount) {
			t.Errorf("%s: err = %v, want the refusal", name, err)
		}
		if len(w.adapter.calls) != 0 {
			t.Errorf("%s: calls = %+v", name, w.adapter.calls)
		}
		for _, id := range []string{w.financeAccount, w.financeTeam, "key-fin", "ref_fin"} {
			if err != nil && strings.Contains(err.Error(), id) {
				t.Errorf("%s: the refusal leaks %q: %v", name, id, err)
			}
		}
	}

	// A pin to the workspace's account works for the person on no team.
	w = newShareWorld(t, nil)
	if _, err := w.stream(t, w.outsider, domain.Pin{AccountID: w.workspace, Model: "m-ws"}); err != nil || w.adapter.keysSpent() != "key-ws" {
		t.Fatalf("workspace pin: keys = %s, %v", w.adapter.keysSpent(), err)
	}
	// An account both teams share works for a member of either.
	for name, pick := range map[string]func(shareWorld) string{
		"finance": func(w shareWorld) string { return w.finance },
		"ops":     func(w shareWorld) string { return w.ops },
	} {
		w := newShareWorld(t, nil)
		if _, err := w.stream(t, pick(w), domain.Pin{AccountID: w.bothAccount, Model: "m-both"}); err != nil || w.adapter.keysSpent() != "key-both" {
			t.Errorf("%s: keys = %s, %v", name, w.adapter.keysSpent(), err)
		}
	}
}

func TestProviderPinPrefersOwnThenTeamThenWorkspace(t *testing.T) {
	// Every account lists "common" and is labeled acme.
	pin := domain.Pin{Provider: "ACME", Model: "common"}
	for name, tc := range map[string]struct {
		user func(shareWorld) string
		key  string
	}{
		"own before a team": {func(w shareWorld) string { return w.both }, "key-mine"},
		// Finance is on Finance: the team accounts b-fin and d-both come
		// before the workspace's a-ws, and b-fin before d-both by name.
		"team before workspace": {func(w shareWorld) string { return w.finance }, "key-fin"},
		"the other team":        {func(w shareWorld) string { return w.ops }, "key-ops"},
		"workspace last":        {func(w shareWorld) string { return w.outsider }, "key-ws"},
	} {
		w := newShareWorld(t, nil)
		if _, err := w.stream(t, tc.user(w), pin); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := w.adapter.keysSpent(); got != tc.key {
			t.Errorf("%s: spent %s, want %s", name, got, tc.key)
		}
	}
}
