package postgres

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// Routes stores and manages gateway routes and their hop chains under row
// security. Every call runs in a transaction carrying the caller's user id,
// permissions, and teams.
type Routes struct {
	Pool *pgxpool.Pool
}

var _ app.RouteStore = Routes{}

func (r Routes) begin(ctx context.Context, who app.Caller) (pgx.Tx, error) {
	return (Providers(r)).begin(ctx, who)
}

const routeColumns = `r.id::text, r.name, r.capability, r.decision, r.installed, r.visibility, COALESCE(r.owner_user_id::text, '')`

const routeVisibleTo = `(r.installed
	OR identity.may_use(r.owner_user_id, r.visibility, r.team_ids)
	OR identity.may_manage(r.owner_user_id, r.visibility))`

const routeChangeable = `(NOT installed AND identity.may_manage(owner_user_id, visibility))`

func scanListedRoute(row pgx.Row) (app.ListedRoute, error) {
	var out app.ListedRoute
	err := row.Scan(&out.ID, &out.Name, &out.Capability, &out.Decision, &out.Installed, &out.Visibility, &out.OwnerUserID)
	out.Hops = []app.ListedHop{}
	out.TeamIDs = []string{}
	out.TeamNames = []string{}
	return out, err
}

// List returns routes visible to who: installed, own private, team-shared,
// everyone-shared, and (for settings:manage) all shared routes.
func (r Routes) List(ctx context.Context, who app.Caller) ([]app.ListedRoute, error) {
	tx, err := r.begin(ctx, who)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT `+routeColumns+`
		FROM gateway.routes AS r
		WHERE `+routeVisibleTo+`
		ORDER BY r.installed DESC,
			CASE
				WHEN r.visibility = 'private' THEN 0
				WHEN r.visibility = 'team' THEN 1
				ELSE 2
			END,
			r.name, r.id`)
	if err != nil {
		return nil, err
	}
	out := []app.ListedRoute{}
	byID := map[string]int{}
	var ids []string
	for rows.Next() {
		item, err := scanListedRoute(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		byID[item.ID] = len(out)
		ids = append(ids, item.ID)
		out = append(out, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		hops, err := routeHopsOf(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for id, list := range hops {
			out[byID[id]].Hops = list
		}
		teams, err := routeTeamsOf(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for id, list := range teams {
			out[byID[id]].TeamIDs, out[byID[id]].TeamNames = list.ids, list.names
		}
	}
	return out, tx.Commit(ctx)
}

// Get returns one route visible to who, or app.ErrNotFound.
func (r Routes) Get(ctx context.Context, who app.Caller, id string) (app.ListedRoute, error) {
	if _, err := uuid.Parse(id); err != nil {
		return app.ListedRoute{}, app.ErrNotFound
	}
	tx, err := r.begin(ctx, who)
	if err != nil {
		return app.ListedRoute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	got, err := getRoute(ctx, tx, id)
	if err != nil {
		return app.ListedRoute{}, err
	}
	return got, tx.Commit(ctx)
}

func getRoute(ctx context.Context, q querier, id string) (app.ListedRoute, error) {
	if _, err := uuid.Parse(id); err != nil {
		return app.ListedRoute{}, app.ErrNotFound
	}
	item, err := scanListedRoute(q.QueryRow(ctx, `
		SELECT `+routeColumns+`
		FROM gateway.routes AS r
		WHERE r.id = $1::uuid AND `+routeVisibleTo, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ListedRoute{}, app.ErrNotFound
	}
	if err != nil {
		return app.ListedRoute{}, err
	}
	hops, err := routeHopsOf(ctx, q, []string{item.ID})
	if err != nil {
		return app.ListedRoute{}, err
	}
	if list := hops[item.ID]; list != nil {
		item.Hops = list
	}
	teams, err := routeTeamsOf(ctx, q, []string{item.ID})
	if err != nil {
		return app.ListedRoute{}, err
	}
	if list, ok := teams[item.ID]; ok {
		item.TeamIDs, item.TeamNames = list.ids, list.names
	}
	return item, nil
}

func routeHopsOf(ctx context.Context, q querier, ids []string) (map[string][]app.ListedHop, error) {
	rows, err := q.Query(ctx, `
		SELECT l.route_id::text, l.position, COALESCE(a.adapter, ''), l.model, l.weight, l.retry_budget, l.provider_account_id::text, l.required_capabilities
		FROM gateway.route_links AS l
		LEFT JOIN gateway.provider_accounts AS a ON a.id = l.provider_account_id
		WHERE l.route_id = ANY($1::uuid[])
		ORDER BY l.route_id, l.position`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]app.ListedHop{}
	for rows.Next() {
		var routeID, adapter, model, accountID string
		var pos, weight, retry int
		var need []string
		if err := rows.Scan(&routeID, &pos, &adapter, &model, &weight, &retry, &accountID, &need); err != nil {
			return nil, err
		}
		out[routeID] = append(out[routeID], app.ListedHop{
			Position:             pos,
			Adapter:              adapter,
			Model:                model,
			Weight:               weight,
			Retry:                retry,
			AccountID:            accountID,
			RequiredCapabilities: need,
		})
	}
	return out, rows.Err()
}

func routeTeamsOf(ctx context.Context, q querier, ids []string) (map[string]sharedWith, error) {
	rows, err := q.Query(ctx, `
		SELECT r.id::text, tid::text, team.name
		FROM gateway.routes AS r
		CROSS JOIN LATERAL unnest(r.team_ids) AS tid
		JOIN identity.teams AS team ON team.id = tid
		WHERE r.id = ANY($1::uuid[])
		ORDER BY r.id, team.name, tid`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sharedWith{}
	for rows.Next() {
		var routeID, id, name string
		if err := rows.Scan(&routeID, &id, &name); err != nil {
			return nil, err
		}
		got := out[routeID]
		got.ids, got.names = append(got.ids, id), append(got.names, name)
		out[routeID] = got
	}
	return out, rows.Err()
}

// Create stores a new route, its hops, and its audit event in one transaction.
func (r Routes) Create(ctx context.Context, who app.Caller, spec app.RouteSpec) (app.ListedRoute, error) {
	tx, err := r.begin(ctx, who)
	if err != nil {
		return app.ListedRoute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	teams := normalizeTeamIDs(spec.Visibility, spec.TeamIDs)
	if err := knownTeams(ctx, tx, teams); err != nil {
		return app.ListedRoute{}, err
	}
	if err := checkPrivateNameCollision(ctx, tx, spec.Visibility, spec.Name, ""); err != nil {
		return app.ListedRoute{}, err
	}
	if err := checkHopAccounts(ctx, tx, spec.Visibility, spec.Hops); err != nil {
		return app.ListedRoute{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO gateway.routes (owner_user_id, name, capability, decision, visibility, team_ids, installed)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid[], false)
		RETURNING id::text`,
		who.User, spec.Name, spec.Capability, spec.Decision, spec.Visibility, teams).Scan(&id)
	if err != nil {
		return app.ListedRoute{}, mapRouteWrite(err)
	}
	if err := putRouteLinks(ctx, tx, id, spec.Hops); err != nil {
		return app.ListedRoute{}, err
	}
	made, err := getRoute(ctx, tx, id)
	if err != nil {
		return app.ListedRoute{}, err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.RouteCreate,
		Outcome:  audit.Success,
		TargetID: id,
		Changes: audit.Changes{
			Fields: []string{"capability", "decision", "team_ids", "visibility"},
			Values: map[string]audit.Change{
				"capability": {To: made.Capability},
				"decision":   {To: made.Decision},
				"visibility": {To: made.Visibility},
				"team_ids":   {To: made.TeamIDs},
			},
		},
	}); err != nil {
		return app.ListedRoute{}, err
	}
	return made, tx.Commit(ctx)
}

// Update modifies a non-installed route's fields and optionally replaces its hops.
func (r Routes) Update(ctx context.Context, who app.Caller, id string, spec app.RouteSpec) (app.ListedRoute, error) {
	if _, err := uuid.Parse(id); err != nil {
		return app.ListedRoute{}, app.ErrNotFound
	}
	tx, err := r.begin(ctx, who)
	if err != nil {
		return app.ListedRoute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	teams := normalizeTeamIDs(spec.Visibility, spec.TeamIDs)
	if err := knownTeams(ctx, tx, teams); err != nil {
		return app.ListedRoute{}, err
	}
	var cur app.ListedRoute
	var curTeams []string
	err = tx.QueryRow(ctx, `
		SELECT name, capability, decision, visibility,
		       COALESCE((SELECT array_agg(tid::text ORDER BY tid) FROM unnest(team_ids) AS tid), '{}'),
		       COALESCE(owner_user_id::text, '')
		FROM gateway.routes
		WHERE id = $1::uuid AND `+routeChangeable+`
		FOR UPDATE`, id).
		Scan(&cur.Name, &cur.Capability, &cur.Decision, &cur.Visibility, &curTeams, &cur.OwnerUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ListedRoute{}, app.ErrNotFound
	}
	if err != nil {
		return app.ListedRoute{}, err
	}
	curHopsMap, err := routeHopsOf(ctx, tx, []string{id})
	if err != nil {
		return app.ListedRoute{}, err
	}
	cur.Hops = curHopsMap[id]
	if err := checkPrivateNameCollision(ctx, tx, spec.Visibility, spec.Name, id); err != nil {
		return app.ListedRoute{}, err
	}
	if spec.HopsSet {
		if err := checkHopAccounts(ctx, tx, spec.Visibility, spec.Hops); err != nil {
			return app.ListedRoute{}, err
		}
	} else if spec.Visibility != domain.VisibilityPrivate {
		if err := checkExistingRouteAccountsShared(ctx, tx, id); err != nil {
			return app.ListedRoute{}, err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.routes
		SET name = $2, capability = $3, decision = $4, visibility = $5, team_ids = $6::uuid[], updated_at = now()
		WHERE id = $1::uuid AND `+routeChangeable,
		id, spec.Name, spec.Capability, spec.Decision, spec.Visibility, teams)
	if err != nil {
		return app.ListedRoute{}, mapRouteWrite(err)
	}
	if tag.RowsAffected() == 0 {
		return app.ListedRoute{}, app.ErrNotFound
	}
	if spec.HopsSet {
		if _, err := tx.Exec(ctx, `DELETE FROM gateway.route_links WHERE route_id = $1::uuid`, id); err != nil {
			return app.ListedRoute{}, err
		}
		if err := putRouteLinks(ctx, tx, id, spec.Hops); err != nil {
			return app.ListedRoute{}, err
		}
	}
	made, err := getRoute(ctx, tx, id)
	if err != nil {
		return app.ListedRoute{}, err
	}
	changes, share := routeChanges(cur, curTeams, made)
	if changes.Fields != nil {
		if err := audit.Record(ctx, tx, audit.Event{
			Action:   audit.RouteUpdate,
			Outcome:  audit.Success,
			TargetID: id,
			Changes:  changes,
		}); err != nil {
			return app.ListedRoute{}, err
		}
	}
	if share.Fields != nil {
		if err := audit.Record(ctx, tx, audit.Event{
			Action:   audit.RouteShare,
			Outcome:  audit.Success,
			TargetID: id,
			Changes:  share,
		}); err != nil {
			return app.ListedRoute{}, err
		}
	}
	if changes.Fields == nil && share.Fields == nil {
		audit.NoChange(ctx)
	}
	return made, tx.Commit(ctx)
}

// Share updates a non-installed route's visibility and teams in one transaction.
func (r Routes) Share(ctx context.Context, who app.Caller, id, visibility string, teamIDs []string) (app.ListedRoute, error) {
	if _, err := uuid.Parse(id); err != nil {
		return app.ListedRoute{}, app.ErrNotFound
	}
	tx, err := r.begin(ctx, who)
	if err != nil {
		return app.ListedRoute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	teams := normalizeTeamIDs(visibility, teamIDs)
	if err := knownTeams(ctx, tx, teams); err != nil {
		return app.ListedRoute{}, err
	}
	var curName, curVis string
	var curTeams []string
	err = tx.QueryRow(ctx, `
		SELECT name, visibility,
		       COALESCE((SELECT array_agg(tid::text ORDER BY tid) FROM unnest(team_ids) AS tid), '{}')
		FROM gateway.routes
		WHERE id = $1::uuid AND `+routeChangeable+`
		FOR UPDATE`, id).Scan(&curName, &curVis, &curTeams)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ListedRoute{}, app.ErrNotFound
	}
	if err != nil {
		return app.ListedRoute{}, err
	}
	if err := checkPrivateNameCollision(ctx, tx, visibility, curName, id); err != nil {
		return app.ListedRoute{}, err
	}
	if visibility != domain.VisibilityPrivate {
		if err := checkExistingRouteAccountsShared(ctx, tx, id); err != nil {
			return app.ListedRoute{}, err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.routes
		SET visibility = $2, team_ids = $3::uuid[], updated_at = now()
		WHERE id = $1::uuid AND `+routeChangeable, id, visibility, teams)
	if err != nil {
		return app.ListedRoute{}, mapRouteWrite(err)
	}
	if tag.RowsAffected() == 0 {
		return app.ListedRoute{}, app.ErrNotFound
	}
	made, err := getRoute(ctx, tx, id)
	if err != nil {
		return app.ListedRoute{}, err
	}
	fromTeams, toTeams := sortedTeams(curTeams), sortedTeams(made.TeamIDs)
	if curVis != made.Visibility || !slices.Equal(fromTeams, toTeams) {
		if err := audit.Record(ctx, tx, audit.Event{
			Action:   audit.RouteShare,
			Outcome:  audit.Success,
			TargetID: id,
			Changes: audit.Changes{
				Fields: []string{"team_ids", "visibility"},
				Values: map[string]audit.Change{
					"visibility": {From: curVis, To: made.Visibility},
					"team_ids":   {From: fromTeams, To: toTeams},
				},
			},
		}); err != nil {
			return app.ListedRoute{}, err
		}
	} else {
		audit.NoChange(ctx)
	}
	return made, tx.Commit(ctx)
}

// Remove deletes a non-installed route and its links in one transaction.
func (r Routes) Remove(ctx context.Context, who app.Caller, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return app.ErrNotFound
	}
	tx, err := r.begin(ctx, who)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var found string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM gateway.routes
		WHERE id = $1::uuid AND `+routeChangeable+`
		FOR UPDATE`, id).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.route_links WHERE route_id = $1::uuid`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.routes WHERE id = $1::uuid AND `+routeChangeable, id); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.RouteDelete,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func normalizeTeamIDs(visibility string, teamIDs []string) []string {
	if visibility != domain.VisibilityTeam {
		return []string{}
	}
	if teamIDs == nil {
		return []string{}
	}
	return teamIDs
}

// checkPrivateNameCollision prevents a private route from reusing the name of
// an existing installed or shared route.
func checkPrivateNameCollision(ctx context.Context, tx pgx.Tx, visibility, name, excludeID string) error {
	if visibility != domain.VisibilityPrivate {
		return nil
	}
	var taken bool
	var err error
	if excludeID == "" {
		err = tx.QueryRow(ctx, `SELECT gateway.shared_route_name_taken($1, NULL)`, name).Scan(&taken)
	} else {
		err = tx.QueryRow(ctx, `SELECT gateway.shared_route_name_taken($1, $2::uuid)`, name, excludeID).Scan(&taken)
	}
	if err != nil {
		return err
	}
	if taken {
		return domain.Invalid("That route name is already in use by a shared or installed route.")
	}
	return nil
}

// checkHopAccounts verifies the caller may use every hop's provider account
// and that a shared route never references a private provider account.
func checkHopAccounts(ctx context.Context, tx pgx.Tx, routeVisibility string, hops []domain.RouteHop) error {
	ids := make([]string, 0, len(hops))
	seen := make(map[string]bool, len(hops))
	for _, h := range hops {
		if !seen[h.AccountID] {
			seen[h.AccountID] = true
			ids = append(ids, h.AccountID)
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, visibility, identity.may_use(owner_user_id, visibility, team_ids)
		FROM gateway.provider_accounts
		WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	type acctInfo struct {
		vis    string
		canUse bool
	}
	found := make(map[string]acctInfo, len(ids))
	for rows.Next() {
		var id, vis string
		var canUse bool
		if err := rows.Scan(&id, &vis, &canUse); err != nil {
			return err
		}
		found[id] = acctInfo{vis: vis, canUse: canUse}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		info, ok := found[id]
		if !ok {
			return domain.Invalid("Choose provider accounts you can use.")
		}
		if !info.canUse {
			return app.ErrRouteAccountForbidden
		}
		if routeVisibility != domain.VisibilityPrivate && info.vis == domain.VisibilityPrivate {
			return domain.Invalid("A shared route cannot use a private provider account.")
		}
	}
	return nil
}

func checkExistingRouteAccountsShared(ctx context.Context, tx pgx.Tx, routeID string) error {
	var hasPrivate bool
	if err := tx.QueryRow(ctx, `SELECT gateway.route_has_private_account($1::uuid)`, routeID).Scan(&hasPrivate); err != nil {
		return err
	}
	if hasPrivate {
		return domain.Invalid("A shared route cannot use a private provider account.")
	}
	return nil
}

func putRouteLinks(ctx context.Context, tx pgx.Tx, routeID string, hops []domain.RouteHop) error {
	for _, h := range hops {
		need := h.RequiredCapabilities
		if need == nil {
			need = []string{}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, weight, retry_budget, required_capabilities)
			VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6, $7::text[])`,
			routeID, h.Position, h.AccountID, h.Model, h.Weight, h.Retry, need); err != nil {
			return err
		}
	}
	return nil
}

func routeChanges(before app.ListedRoute, beforeTeams []string, after app.ListedRoute) (changes, share audit.Changes) {
	put := func(into *audit.Changes, field string, from, to any) {
		into.Fields = append(into.Fields, field)
		if into.Values == nil {
			into.Values = map[string]audit.Change{}
		}
		into.Values[field] = audit.Change{From: from, To: to}
	}
	named := func(into *audit.Changes, field string) {
		into.Fields = append(into.Fields, field)
	}
	if before.Name != after.Name {
		named(&changes, "name")
	}
	if before.Capability != after.Capability {
		put(&changes, "capability", before.Capability, after.Capability)
	}
	if before.Decision != after.Decision {
		put(&changes, "decision", before.Decision, after.Decision)
	}
	if !hopsEqual(before.Hops, after.Hops) {
		named(&changes, "hops")
	}
	fromTeams, toTeams := sortedTeams(beforeTeams), sortedTeams(after.TeamIDs)
	if before.Visibility != after.Visibility || !slices.Equal(fromTeams, toTeams) {
		put(&changes, "visibility", before.Visibility, after.Visibility)
		put(&changes, "team_ids", fromTeams, toTeams)
		put(&share, "visibility", before.Visibility, after.Visibility)
		put(&share, "team_ids", fromTeams, toTeams)
	}
	sort.Strings(changes.Fields)
	sort.Strings(share.Fields)
	return changes, share
}

func hopsEqual(a, b []app.ListedHop) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Position != b[i].Position ||
			a[i].AccountID != b[i].AccountID ||
			a[i].Model != b[i].Model ||
			a[i].Weight != b[i].Weight ||
			a[i].Retry != b[i].Retry ||
			!slices.Equal(a[i].RequiredCapabilities, b[i].RequiredCapabilities) {
			return false
		}
	}
	return true
}

func mapRouteWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(strings.Contains(pgErr.ConstraintName, "routes_private_name") || strings.Contains(pgErr.ConstraintName, "routes_shared_name")) {
		return app.ErrRouteDuplicate
	}
	return err
}
