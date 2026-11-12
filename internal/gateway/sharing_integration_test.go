//go:build integration

package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/identity"
)

// person adds a joined member of the install with a system role.
func (r *rig) person(name, role string) string {
	r.t.Helper()
	var id string
	err := r.owner.QueryRow(context.Background(), `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, $2, (SELECT id FROM identity.roles WHERE key = $3), now())
		RETURNING id::text`, name+"@example.com", name, role).Scan(&id)
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

// team adds a team and returns its id.
func (r *rig) team(name string) string {
	r.t.Helper()
	var id string
	if err := r.owner.QueryRow(context.Background(), `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// join puts a person on a team.
func (r *rig) join(team, person string) {
	r.t.Helper()
	if _, err := r.owner.Exec(context.Background(), `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, team, person); err != nil {
		r.t.Fatal(err)
	}
}

// tryAs runs one statement the way a request of this person would see the
// database: as upmo_app, with the person (and, if allow is set, their
// permissions) named in the transaction. It rolls back and says how many rows
// the statement touched.
func (r *rig) tryAs(user, allow, sql string, args ...any) (int64, error) {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.app.Begin(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.asPerson(ctx, tx, user, allow); err != nil {
		r.t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

// seenBy counts the rows of a gateway table a person's request can read.
func (r *rig) seenBy(user, allow, table string) int {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.app.Begin(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.asPerson(ctx, tx, user, allow); err != nil {
		r.t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway.`+table).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// asPerson names the person, their permissions and their current teams on the
// transaction, which is what a request writes before it touches a row.
func (r *rig) asPerson(ctx context.Context, tx pgx.Tx, user, allow string) error {
	teams := "{}"
	if user != "" {
		var got *string
		if err := r.owner.QueryRow(ctx, `
			SELECT array_agg(team_id ORDER BY team_id)::text
			FROM identity.team_members WHERE user_id = $1::uuid`, user).Scan(&got); err != nil {
			return err
		}
		if got != nil {
			teams = *got
		}
	}
	_, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
			set_config('upmo.allow', $2, true),
			set_config('upmo.teams', $3, true)`, user, allow, teams)
	return err
}

// crowd is a workspace with an admin, members of two teams, a member of both
// and a member of none, and the provider routes as brain mounts them with
// sharing decided by the person's role.
type crowd struct {
	*rig
	admin, finance, ops, both, outsider string
	financeTeam, opsTeam                string
}

func newCrowd(t *testing.T) *crowd {
	t.Helper()
	r := newRig(t, masterKeys(t, 'a'))
	members := identity.Members(r.app, "", nil, nil)
	// Sharing is for the roles that hold settings:manage, as brain wires it.
	r.svc.Allow = func(ctx context.Context, user string) (string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return "", err
		}
		return members.SettingsAllow(ctx, person)
	}
	r.svc.Teams = func(ctx context.Context, user string) ([]string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return nil, err
		}
		return members.TeamsOf(ctx, person)
	}
	r.svc.Names = func(ctx context.Context, teams []string) (map[string]string, error) {
		_, names, err := members.NamesOf(ctx, nil, teams)
		return names, err
	}
	r.mount(nil)
	c := &crowd{rig: r}
	// An install always has a joined owner.
	r.person("founder", "owner")
	c.admin = r.person("admin", "admin")
	c.finance = r.person("finance", "member")
	c.ops = r.person("ops", "member")
	c.both = r.person("both", "member")
	c.outsider = r.person("outsider", "member")
	c.financeTeam = r.team("Finance")
	c.opsTeam = r.team("Ops")
	r.join(c.financeTeam, c.finance)
	r.join(c.opsTeam, c.ops)
	r.join(c.financeTeam, c.both)
	r.join(c.opsTeam, c.both)
	return c
}

// shared is the body of a shared account. teams may be empty for everyone.
func shared(name, baseURL, key, visibility string, teams ...string) map[string]any {
	b := body(name, baseURL, key)
	b["visibility"] = visibility
	if len(teams) > 0 {
		b["team_ids"] = teams
	}
	return b
}

func names(rows []account) string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.Name
	}
	return strings.Join(out, ",")
}

func TestAnAdminSharesAnAccountWithATeamAndOnlyItsMembersSeeIt(t *testing.T) {
	c := newCrowd(t)
	prov := newFakeProvider(t)
	made := c.mustAdd(c.admin, shared("Finance models", prov.url(), adaKey, "team", c.financeTeam))
	if made.Visibility != "team" || !made.CanManage || made.CanUse || len(made.UsableModels) != 0 ||
		len(made.TeamIDs) != 1 || made.TeamIDs[0] != c.financeTeam ||
		len(made.TeamNames) != 1 || made.TeamNames[0] != "Finance" || made.Name != "Finance models" {
		t.Fatalf("made = %+v", made)
	}

	// A member of the team sees the account and its models, and may not change it.
	rows := c.list(c.finance)
	if len(rows) != 1 || rows[0].ID != made.ID || rows[0].Visibility != "team" || rows[0].CanManage || !rows[0].CanUse ||
		strings.Join(rows[0].Models, ",") != "m1,m2" || rows[0].PlanKind != "local" {
		t.Fatalf("finance sees %+v", rows)
	}
	for _, c2 := range []struct{ method, path string }{
		{http.MethodPut, "/api/providers/" + made.ID},
		{http.MethodDelete, "/api/providers/" + made.ID},
		{http.MethodPost, "/api/providers/test"},
	} {
		code, raw := c.call(c.finance, c2.method, c2.path, map[string]any{"id": made.ID, "name": "mine now", "api_key": newKey})
		if code != http.StatusForbidden {
			t.Errorf("finance %s %s = %d %s", c2.method, c2.path, code, raw)
		}
	}

	// Everyone else gets not found, whatever they ask.
	for name, who := range map[string]string{"ops": c.ops, "outsider": c.outsider} {
		if rows := c.list(who); len(rows) != 0 {
			t.Errorf("%s sees %+v", name, rows)
		}
		for _, c2 := range []struct{ method, path string }{
			{http.MethodPut, "/api/providers/" + made.ID},
			{http.MethodDelete, "/api/providers/" + made.ID},
			{http.MethodPost, "/api/providers/test"},
		} {
			code, raw := c.call(who, c2.method, c2.path, map[string]any{"id": made.ID, "name": "mine now", "api_key": newKey})
			if code != http.StatusNotFound {
				t.Errorf("%s %s %s = %d %s", name, c2.method, c2.path, code, raw)
			}
		}
	}
	if prov.hits.Load() != 0 {
		t.Fatal("a refused test reached the provider with the shared key")
	}

	// The admin sees and manages it, and nothing changed.
	rows = c.list(c.admin)
	if len(rows) != 1 || !rows[0].CanManage || rows[0].Name != "Finance models" {
		t.Fatalf("admin sees %+v", rows)
	}

	// Row security says the same below the handler: the member reads the
	// account and its models and never the sealed key.
	for table, want := range map[string][3]int{
		"provider_accounts": {1, 0, 0},
		"provider_models":   {2, 0, 0},
		"credentials":       {0, 0, 0},
	} {
		if got := [3]int{c.seenBy(c.finance, "", table), c.seenBy(c.ops, "", table), c.seenBy(c.outsider, "", table)}; got != want {
			t.Errorf("%s seen by finance, ops, outsider = %v, want %v", table, got, want)
		}
	}
	if n := c.seenBy("", "", "provider_accounts"); n != 0 {
		t.Errorf("a request with no person sees %d team accounts", n)
	}
	ref := refOf(t, c.rig, made.ID)
	if _, err := (postgres.Providers{Pool: c.app}).Sealed(context.Background(), app.Caller{User: c.finance}, ref); err == nil {
		t.Fatal("a member of the team read the sealed key")
	}
	// The admin's request reaches the key, to replace it or to test it.
	if n := c.seenBy(c.admin, "settings:manage", "credentials"); n != 1 {
		t.Errorf("the admin sees %d credentials", n)
	}
	c.assertNoSecrets(adaKey, newKey, ref)
}

func TestAWorkspaceAccountIsSeenByEveryoneAndChangedByAdminsOnly(t *testing.T) {
	c := newCrowd(t)
	prov := newFakeProvider(t)
	made := c.mustAdd(c.admin, shared("Everyone models", prov.url(), adaKey, "everyone"))
	if made.Visibility != "everyone" || !made.CanManage || !made.CanUse || len(made.TeamIDs) != 0 {
		t.Fatalf("made = %+v", made)
	}
	if n := c.count(`SELECT count(*) FROM gateway.provider_accounts WHERE visibility = 'everyone' AND cardinality(team_ids) > 0`); n != 0 {
		t.Fatalf("a workspace account lists %d team sets", n)
	}
	for name, who := range map[string]string{"finance": c.finance, "ops": c.ops, "both": c.both, "outsider": c.outsider} {
		rows := c.list(who)
		if len(rows) != 1 || rows[0].ID != made.ID || rows[0].Visibility != "everyone" || rows[0].CanManage || !rows[0].CanUse {
			t.Errorf("%s sees %+v", name, rows)
		}
		if code, raw := c.call(who, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"name": "mine now"}); code != http.StatusForbidden {
			t.Errorf("%s update = %d %s", name, code, raw)
		}
	}
	// Nobody but the admin reads its key.
	for _, who := range []string{c.finance, c.outsider} {
		if n := c.seenBy(who, "", "credentials"); n != 0 {
			t.Errorf("a member sees %d credentials", n)
		}
	}
	c.assertNoSecrets(adaKey)
}

func TestTwoTeamsGiveTheUnionAndAnAccountReachedTwiceAppearsOnce(t *testing.T) {
	c := newCrowd(t)
	prov := newFakeProvider(t)
	c.mustAdd(c.admin, shared("A finance", prov.url(), "", "team", c.financeTeam))
	c.mustAdd(c.admin, shared("B ops", prov.url(), "", "team", c.opsTeam))
	c.mustAdd(c.admin, shared("C both", prov.url(), "", "team", c.financeTeam, c.opsTeam))
	c.mustAdd(c.admin, shared("D workspace", prov.url(), "", "everyone"))
	own := c.mustAdd(c.both, body("E mine", prov.url(), ""))
	if own.Visibility != "private" || !own.CanManage || !own.CanUse {
		t.Fatalf("own = %+v", own)
	}

	// Own first, then the teams', then the workspace's, each by name; the
	// account both teams share is there once.
	if got := names(c.list(c.both)); got != "E mine,A finance,B ops,C both,D workspace" {
		t.Errorf("both sees %s", got)
	}
	if got := names(c.list(c.finance)); got != "A finance,C both,D workspace" {
		t.Errorf("finance sees %s", got)
	}
	if got := names(c.list(c.ops)); got != "B ops,C both,D workspace" {
		t.Errorf("ops sees %s", got)
	}
	if got := names(c.list(c.outsider)); got != "D workspace" {
		t.Errorf("outsider sees %s", got)
	}
	// The admin manages every shared one and none of the personal ones.
	if got := names(c.list(c.admin)); got != "A finance,B ops,C both,D workspace" {
		t.Errorf("admin sees %s", got)
	}
	// Someone's personal account is nobody else's, even the admin's.
	for _, row := range c.list(c.admin) {
		if row.ID == own.ID {
			t.Errorf("the admin sees a personal account: %+v", row)
		}
	}

	// is_owner is only a display flag: true for the owner of a private account
	// and of a shared one, false for another admin and for a house account.
	if !own.IsOwner {
		t.Fatal("the owner of a private account was not marked")
	}
	for _, row := range c.list(c.admin) {
		if !row.IsOwner {
			t.Errorf("the owner of shared %q was not marked", row.Name)
		}
	}
	for _, row := range c.list(c.both) {
		want := row.ID == own.ID
		if row.IsOwner != want {
			t.Errorf("%s is_owner = %v, want %v", row.Name, row.IsOwner, want)
		}
	}
	other := c.person("other-admin", "admin")
	otherRows := c.list(other)
	if names(otherRows) != "A finance,B ops,C both,D workspace" {
		t.Fatalf("another admin sees %s", names(otherRows))
	}
	for _, row := range otherRows {
		if row.IsOwner {
			t.Errorf("another admin is marked owner of %q", row.Name)
		}
	}
	c.globalAccount("House install")
	var house account
	var found bool
	for _, row := range c.list(c.admin) {
		if row.Name == "House install" {
			house, found = row, true
		}
	}
	if !found || house.IsOwner {
		t.Fatalf("house account = %+v, found %v", house, found)
	}
}

func TestOnlyAnAdminSharesAccounts(t *testing.T) {
	c := newCrowd(t)
	prov := newFakeProvider(t)
	for name, b := range map[string]map[string]any{
		"everyone": shared("Mine for all", prov.url(), "", "everyone"),
		"team":     shared("Mine for finance", prov.url(), "", "team", c.financeTeam),
		"teams, no visibility": func() map[string]any {
			b := body("Sneaky", prov.url(), "")
			b["team_ids"] = []string{c.financeTeam}
			return b
		}(),
	} {
		code, raw := c.add(c.finance, b)
		if code != http.StatusForbidden || message(t, raw) != "Only workspace admins can share a provider." {
			t.Errorf("member shares %s = %d %s", name, code, raw)
		}
	}
	if n := c.count(`SELECT count(*) FROM gateway.provider_accounts`); n != 0 {
		t.Fatalf("a refused share wrote %d accounts", n)
	}
	// A member's own account stays theirs, and cannot be turned into a shared one.
	mine := c.mustAdd(c.finance, body("Plain", prov.url(), ""))
	if code, raw := c.call(c.finance, http.MethodPut, "/api/providers/"+mine.ID, map[string]any{"visibility": "everyone"}); code != http.StatusForbidden {
		t.Errorf("a personal account was shared by its owner: %d %s", code, raw)
	}
	if rows := c.list(c.outsider); len(rows) != 0 {
		t.Errorf("the outsider sees %+v", rows)
	}

	// An admin can.
	made := c.mustAdd(c.admin, shared("For finance", prov.url(), "", "team", c.financeTeam))
	if made.Visibility != "team" {
		t.Fatalf("made = %+v", made)
	}
	// An admin cannot name a team that does not exist, or none, or a workspace
	// with teams.
	for name, b := range map[string]map[string]any{
		"unknown team":          shared("X1", prov.url(), "", "team", uuid.NewString()),
		"not a team id":         shared("X2", prov.url(), "", "team", "finance"),
		"team with none":        shared("X3", prov.url(), "", "team"),
		"everyone with teams":   shared("X4", prov.url(), "", "everyone", c.financeTeam),
		"unknown visibility":    shared("X5", prov.url(), "", "workspace"),
	} {
		if code, raw := c.add(c.admin, b); code != http.StatusUnprocessableEntity || message(t, raw) == "" {
			t.Errorf("%s = %d %s", name, code, raw)
		}
	}
	if n := c.count(`SELECT count(*) FROM gateway.provider_accounts WHERE visibility <> 'private'`); n != 1 {
		t.Fatalf("shared accounts = %d, want the one made", n)
	}

	// Row security refuses a write a member makes around the handler, even
	// when no one checked the permission before it.
	if _, err := c.tryAs(c.finance, "", `
		INSERT INTO gateway.provider_accounts (owner_user_id, name, adapter, credential_ref, plan_kind, house, visibility, team_ids)
		VALUES ($1::uuid, 'forged', 'fake', 'forged_ref', 'local', false, 'team', ARRAY[$2]::uuid[])`, c.finance, c.opsTeam); err == nil {
		t.Error("a member inserted a shared account")
	}
	if n, err := c.tryAs(c.finance, "", `UPDATE gateway.provider_accounts SET team_ids = ARRAY[$1]::uuid[] WHERE name = 'For finance'`, c.opsTeam); err != nil || n != 0 {
		t.Errorf("a member retargeted %d shared accounts: %v", n, err)
	}
	if n, err := c.tryAs(c.finance, "", `UPDATE gateway.provider_accounts SET name = 'taken' WHERE name = 'For finance'`); err != nil || n != 0 {
		t.Errorf("a member updated %d shared accounts: %v", n, err)
	}
	if n, err := c.tryAs(c.finance, "", `DELETE FROM gateway.provider_accounts WHERE name = 'For finance'`); err != nil || n != 0 {
		t.Errorf("a member deleted %d shared accounts: %v", n, err)
	}
	// The same statement works with the permission.
	if n, err := c.tryAs(c.admin, "settings:manage", `UPDATE gateway.provider_accounts SET name = 'renamed' WHERE name = 'For finance'`); err != nil || n != 1 {
		t.Errorf("an admin updated %d shared accounts: %v", n, err)
	}
}

func TestAnAdminChangesWhoAnAccountIsSharedWithAndRemovesIt(t *testing.T) {
	c := newCrowd(t)
	prov := newFakeProvider(t)
	made := c.mustAdd(c.admin, shared("Moving", prov.url(), adaKey, "team", c.financeTeam))

	// Finance to Ops.
	code, raw := c.call(c.admin, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"visibility": "team", "team_ids": []string{c.opsTeam}})
	if code != http.StatusOK {
		t.Fatalf("move = %d %s", code, raw)
	}
	if got := decode[account](t, raw); got.Visibility != "team" || len(got.TeamIDs) != 1 || got.TeamIDs[0] != c.opsTeam {
		t.Fatalf("moved = %+v", got)
	}
	if len(c.list(c.finance)) != 0 || len(c.list(c.ops)) != 1 {
		t.Fatalf("after the move finance sees %d, ops %d", len(c.list(c.finance)), len(c.list(c.ops)))
	}
	// Only the name changes: the sharing stays.
	if code, raw = c.call(c.admin, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"name": "Moved"}); code != http.StatusOK {
		t.Fatalf("rename = %d %s", code, raw)
	}
	if got := decode[account](t, raw); got.Name != "Moved" || got.Visibility != "team" || len(got.TeamIDs) != 1 || got.TeamIDs[0] != c.opsTeam {
		t.Fatalf("renamed = %+v", got)
	}
	// A team-less change is refused and nothing moves.
	if code, raw = c.call(c.admin, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"visibility": "team", "team_ids": []string{}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("no teams = %d %s", code, raw)
	}
	if len(c.list(c.ops)) != 1 {
		t.Fatal("a refused change moved the account")
	}
	// To the whole workspace: the team rows go.
	if code, raw = c.call(c.admin, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"visibility": "everyone"}); code != http.StatusOK {
		t.Fatalf("to everyone = %d %s", code, raw)
	}
	if got := decode[account](t, raw); got.Visibility != "everyone" || len(got.TeamIDs) != 0 {
		t.Fatalf("workspace = %+v", got)
	}
	if n := c.count(`SELECT count(*) FROM gateway.provider_accounts WHERE cardinality(team_ids) > 0`); n != 0 {
		t.Fatalf("team ids left on %d accounts", n)
	}
	for _, who := range []string{c.finance, c.outsider} {
		if len(c.list(who)) != 1 {
			t.Fatalf("after going to the workspace %s sees %+v", who, c.list(who))
		}
	}
	// A new key and a test by the admin work on a shared account.
	if code, raw = c.call(c.admin, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"api_key": newKey}); code != http.StatusOK {
		t.Fatalf("new key = %d %s", code, raw)
	}
	if code, raw = c.call(c.admin, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID}); code != http.StatusOK || prov.lastAuth() != "Bearer "+newKey {
		t.Fatalf("test = %d %s, the provider saw %q", code, raw, prov.lastAuth())
	}

	// Back to a team, then remove: models, shares and key go too.
	if code, raw = c.call(c.admin, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"visibility": "team", "team_ids": []string{c.opsTeam, c.financeTeam}}); code != http.StatusOK {
		t.Fatalf("back to teams = %d %s", code, raw)
	}
	if code, raw = c.call(c.admin, http.MethodDelete, "/api/providers/"+made.ID, nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", code, raw)
	}
	for _, q := range []string{
		`SELECT count(*) FROM gateway.provider_accounts`,
		`SELECT count(*) FROM gateway.provider_models`,
		`SELECT count(*) FROM gateway.credentials`,
	} {
		if n := c.count(q); n != 0 {
			t.Errorf("%s = %d after the remove", q, n)
		}
	}
	c.assertNoSecrets(adaKey, newKey)
}

func TestSharedAccountsKeepTheirNamesAndRefsUnique(t *testing.T) {
	c := newCrowd(t)
	prov := newFakeProvider(t)
	c.mustAdd(c.admin, shared("Taken", prov.url(), "", "everyone"))
	if code, raw := c.add(c.admin, shared("Taken", prov.url(), "", "team", c.opsTeam)); code != http.StatusConflict {
		t.Errorf("a second shared account with the name = %d %s", code, raw)
	}
	// A personal account is in a scope of its own.
	if made := c.mustAdd(c.finance, body("Taken", prov.url(), "")); made.Visibility != "private" {
		t.Errorf("made = %+v", made)
	}
	// Two shared accounts cannot hold one credential ref, which the personal
	// ones already could not.
	insert := `
		INSERT INTO gateway.provider_accounts (owner_user_id, name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ($2::uuid, $1, 'fake', 'same_ref', 'local', false, 'everyone')`
	if _, err := c.owner.Exec(context.Background(), insert, "ref one", c.admin); err != nil {
		t.Fatal(err)
	}
	if _, err := c.owner.Exec(context.Background(), insert, "ref two", c.admin); err == nil {
		t.Error("two shared accounts share a credential ref")
	}
}

func TestWhoMaySharePerTheServer(t *testing.T) {
	c := newCrowd(t)
	for who, want := range map[string]bool{c.admin: true, c.finance: false, c.outsider: false} {
		code, raw := c.call(who, http.MethodGet, "/api/providers/sharing", nil)
		if code != http.StatusOK || decode[map[string]bool](t, raw)["can_share"] != want {
			t.Errorf("sharing for %s = %d %s, want %v", who, code, raw, want)
		}
	}
	if code, _ := c.call("", http.MethodGet, "/api/providers/sharing", nil); code != http.StatusUnauthorized {
		t.Errorf("no session = %d", code)
	}
}
