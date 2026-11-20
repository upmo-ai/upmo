//go:build integration

package postgres_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	httpapi "github.com/upmo-ai/upmo/internal/gateway/adapters/http"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	usageAdmin  = "019a0000-0000-7000-8000-000000000220"
	usageMember = "019a0000-0000-7000-8000-000000000221"

	usageHouse      = "019a0000-0000-7000-8000-0000000002a0"
	usageAdminSub   = "019a0000-0000-7000-8000-0000000002a1"
	usageMemberOwn  = "019a0000-0000-7000-8000-0000000002a2"
	usageAdminLocal = "019a0000-0000-7000-8000-0000000002a3"

	usageTeam   = "019a0000-0000-7000-8000-0000000002b0"
	usageAgent  = "019a0000-0000-7000-8000-0000000002c0"
	usageParent = "019a0000-0000-7000-8000-0000000002d0"
	usageChild  = "019a0000-0000-7000-8000-0000000002d1"
	usageOther  = "019a0000-0000-7000-8000-0000000002d2"
)

// usageNow is a Thursday noon in UTC; Berlin is one hour ahead in November.
var usageNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

type usageRig struct {
	owner *pgxpool.Pool
	app   *pgxpool.Pool
}

func newUsageRig(t *testing.T) usageRig {
	t.Helper()
	ctx := context.Background()
	owner := pgtest.New(t)
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct{ id, owner, vendor, name, plan string }{
		{usageHouse, "", "anthropic", "Anthropic", domain.PlanMetered},
		{usageAdminSub, usageAdmin, "anthropic", "Admin Claude Max", domain.PlanSubscription},
		{usageMemberOwn, usageMember, "openai", "Member own key", domain.PlanMetered},
		{usageAdminLocal, usageAdmin, "ollama", "Admin Ollama", domain.PlanLocal},
	} {
		if _, err := owner.Exec(ctx, `
			INSERT INTO gateway.provider_accounts
				(id, owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, visibility)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, 'fake', 'ref_' || $1, $5, $2 = '', '',
				CASE WHEN $2 = '' THEN 'everyone' ELSE 'private' END)`,
			a.id, a.owner, a.vendor, a.name, a.plan); err != nil {
			t.Fatal(err)
		}
	}
	// claude-x has an anthropic row; llama only the model-only fallback; and
	// mystery has no row at all.
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.prices (vendor, model, version, input_usd_per_mtok, output_usd_per_mtok, cache_read_usd_per_mtok, cache_write_usd_per_mtok)
		VALUES ('anthropic', 'claude-x', 'v1', 3, 15, 0.3, 3.75),
			('', 'llama', 'v1', 0.5, 1, NULL, NULL)`); err != nil {
		t.Fatal(err)
	}
	return usageRig{owner: owner, app: pgtest.App(t, owner)}
}

type usageCall struct {
	at                    time.Time
	user, account, model  string
	plan, source, failure string
	agent, team           string
	run, parent           string
	in, out, cr, cw, rs   int
	cost                  *int64
	list                  *int64
	estimated             bool
	latency               *int
	charged               bool
}

func micro(v int64) *int64 { return &v }

func ms(v int) *int { return &v }

func (r usageRig) call(t *testing.T, c usageCall) {
	t.Helper()
	if c.plan == "" {
		c.plan = domain.PlanMetered
	}
	if c.source == "" {
		c.source = domain.SourceChat
		if c.user == "" {
			c.source = domain.SourceSystem
		}
	}
	priceSource := ""
	if c.plan == domain.PlanMetered {
		switch {
		case c.estimated:
			priceSource = "estimate"
		case c.cost == nil:
			priceSource = "unknown"
		default:
			priceSource = "provider"
		}
	}
	if _, err := r.owner.Exec(context.Background(), `
		INSERT INTO gateway.ledger (
			at, route, link_position, provider_account_id, model, plan_kind, failure_class,
			user_id, agent_id, owner_team_id, run_id, parent_run_id,
			input_tokens, output_tokens, cache_read, cache_write, reasoning,
			cost_micro_usd, estimated, source, latency_ms, charged_to_limit, price_source, list_cost_micro_usd
		) VALUES (
			$1, 'usage', 1, NULLIF($2, '')::uuid, $3, $4, $5,
			NULLIF($6, '')::uuid, NULLIF($7, '')::uuid, NULLIF($8, '')::uuid, NULLIF($9, '')::uuid, NULLIF($10, '')::uuid,
			$11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, NULLIF($21, ''), $22
		)`,
		c.at, c.account, c.model, c.plan, c.failure,
		c.user, c.agent, c.team, c.run, c.parent,
		c.in, c.out, c.cr, c.cw, c.rs,
		c.cost, c.estimated, c.source, c.latency, c.charged, priceSource, c.list); err != nil {
		t.Fatal(err)
	}
}

// service is the usage use case as brain wires it, with fixed callbacks in
// place of identity, agents and tasks.
func (r usageRig) service() app.UsageService {
	s := gateway.NewUsage(r.app)
	s.Now = func() time.Time { return usageNow }
	s.Allow = func(_ context.Context, user string) (string, error) {
		if user == usageAdmin {
			return "usage:view,settings:manage", nil
		}
		return "", nil
	}
	s.TimeZone = func(context.Context, string) (string, error) { return "Europe/Berlin", nil }
	s.People = func(_ context.Context, ids []string) (map[string]string, error) {
		known := map[string]string{usageAdmin: "Ada Admin", usageMember: "=SUM(1,1)"}
		out := map[string]string{}
		for _, id := range ids {
			if n, ok := known[id]; ok {
				out[id] = n
			}
		}
		return out, nil
	}
	s.TeamNames = func(context.Context, []string) (map[string]string, error) {
		return map[string]string{usageTeam: "Finance"}, nil
	}
	s.Agents = func(context.Context, string, []string) (map[string]string, error) {
		return map[string]string{}, nil
	}
	s.Goals = func(context.Context, string, []string) (map[string]string, error) {
		return map[string]string{usageParent: "Daily finance digest"}, nil
	}
	return s
}

// served is the usage routes behind the audit middleware, signed in as user.
func (r usageRig) served(user string) http.Handler {
	mux := http.NewServeMux()
	httpapi.UsageHandler{
		Service: r.service(),
		Access: func(req *http.Request) (string, bool) {
			audit.WithActor(req.Context(), audit.Actor{Type: "user", ID: user, CredentialType: "session"})
			return user, true
		},
	}.MountUsage(mux, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return audit.Middleware(audit.Routes(), mux, r.app, "brain", nil, log)(mux)
}

func (r usageRig) get(t *testing.T, user, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.served(user).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

type auditRow struct {
	action, outcome, actor string
	changes                map[string]any
}

func (r usageRig) auditRows(t *testing.T) []auditRow {
	t.Helper()
	rows, err := r.owner.Query(context.Background(), `
		SELECT action, outcome, COALESCE(actor_id::text, ''), changes
		FROM audit.pending ORDER BY occurred_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(&a.action, &a.outcome, &a.actor, &a.changes); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func personal(t *testing.T, s app.UsageService, user string, r domain.UsageRange) domain.UsageReport {
	t.Helper()
	got, err := s.Personal(context.Background(), user, r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func bucketAt(t *testing.T, buckets []domain.UsageBucket, at string) domain.UsageBucket {
	t.Helper()
	for _, b := range buckets {
		if b.At == at {
			return b
		}
	}
	t.Fatalf("no bucket %s in %+v", at, buckets)
	return domain.UsageBucket{}
}

func moneyIs(v *int64, want int64) bool { return v != nil && *v == want }

func TestFeature_UPM220_PersonalUsageOwnRowsOnly(t *testing.T) {
	r := newUsageRig(t)
	day := usageNow.Add(-time.Hour)
	r.call(t, usageCall{at: day, user: usageAdmin, account: usageHouse, model: "claude-x", in: 100, cost: micro(1000)})
	r.call(t, usageCall{at: day, user: usageAdmin, account: usageHouse, model: "claude-x", in: 50, cost: micro(500), failure: "timeout"})
	r.call(t, usageCall{at: day, user: usageMember, account: usageHouse, model: "claude-x", in: 7000, cost: micro(70000)})
	r.call(t, usageCall{at: day, account: usageHouse, model: "claude-x", in: 9000, cost: micro(90000)})

	got := personal(t, r.service(), usageAdmin, domain.UsageWeek)
	if !moneyIs(got.SpendMicroUSD, 1500) {
		t.Fatalf("admin spend = %v, want 1500 from their own two calls", got.SpendMicroUSD)
	}
	var requests, errs, input int64
	for _, b := range got.Buckets {
		requests += b.Requests
		errs += b.Errors
		input += b.Input
	}
	if requests != 2 || errs != 1 || input != 150 {
		t.Fatalf("admin buckets = %d requests, %d errors, %d input; want only their own 2, 1, 150", requests, errs, input)
	}
	if len(got.Sources) != 1 || got.Sources[0].Source != domain.SourceChat || got.Sources[0].Requests != 2 {
		t.Fatalf("sources = %+v, want only the admin's chat calls", got.Sources)
	}
}

// TestFeature_UPM220_PersonalReadIsScopedToCaller has a usage:view holder
// read their own usage with two other people's rows and a system row in the
// window. The personal read runs with an empty allow list, so row security
// already hides the others on the app pool; the store is also read on the
// owner pool, where row security does not apply, so the query's own user
// filter is what keeps the other rows out.
func TestFeature_UPM220_PersonalReadIsScopedToCaller(t *testing.T) {
	ctx := context.Background()
	r := newUsageRig(t)
	const second = "019a0000-0000-7000-8000-000000000222"
	at := usageNow.Add(-time.Hour)
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", in: 100, cost: micro(1000), latency: ms(500)})
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", source: domain.SourceTask, run: usageParent, in: 10, cost: micro(100), latency: ms(700)})
	r.call(t, usageCall{at: at, user: usageMember, account: usageHouse, model: "claude-x", in: 7000, cost: micro(70000), latency: ms(9000)})
	r.call(t, usageCall{at: at, user: usageMember, account: usageHouse, model: "claude-x", source: domain.SourceTask, run: usageOther, in: 50, cost: micro(500), latency: ms(9000)})
	r.call(t, usageCall{at: at, user: second, account: usageHouse, model: "claude-x", in: 4000, cost: micro(40000), latency: ms(9000)})
	r.call(t, usageCall{at: at, account: usageHouse, model: "claude-x", in: 9000, cost: micro(90000)})

	s := r.service()
	got := personal(t, s, usageAdmin, domain.UsageWeek)
	var requests, input int64
	for _, b := range got.Buckets {
		requests += b.Requests
		input += b.Input
	}
	if requests != 2 || input != 110 || !moneyIs(got.SpendMicroUSD, 1100) {
		t.Fatalf("admin personal = %d requests, %d input, spend %v; want only their own 2, 110, 1100", requests, input, got.SpendMicroUSD)
	}
	sources := map[string]int64{}
	for _, src := range got.Sources {
		sources[src.Source] = src.Requests
	}
	if len(sources) != 2 || sources[domain.SourceChat] != 1 || sources[domain.SourceTask] != 1 {
		t.Fatalf("admin sources = %+v, want their own chat and task call", got.Sources)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].TaskID != usageParent {
		t.Fatalf("admin tasks = %+v, want only their own task", got.Tasks)
	}

	ws, err := s.Workspace(ctx, usageAdmin, domain.UsageWeek, domain.GroupPerson, "json")
	if err != nil {
		t.Fatal(err)
	}
	people := map[string]int64{}
	for _, row := range ws.Rows {
		people[row.ID] = row.Requests
	}
	if people[usageAdmin] != 2 || people[usageMember] != 2 || people[second] != 1 || people[""] != 1 || !moneyIs(ws.SpendMicroUSD, 201600) {
		t.Fatalf("admin workspace = %+v spend %v, want every person and System, 201600 in all", ws.Rows, ws.SpendMicroUSD)
	}

	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	w := domain.UsageWindowFor(domain.UsageWeek, usageNow, zone)
	// The owner pool reads past row security, as a usage:view holder does.
	owner := postgres.Usage{Pool: r.owner}
	all, err := owner.WorkspaceUsage(ctx, app.Caller{User: usageAdmin}, w, domain.GroupPerson)
	if err != nil {
		t.Fatal(err)
	}
	var allRequests int64
	for _, c := range all.Buckets {
		allRequests += c.Requests
	}
	if allRequests != 6 {
		t.Fatalf("owner workspace = %d requests, want all 6 rows reachable", allRequests)
	}
	scan, err := owner.PersonalUsage(ctx, app.Caller{User: usageAdmin, Allow: "usage:view,settings:manage"}, w)
	if err != nil {
		t.Fatal(err)
	}
	var bucketRequests, bucketInput, keyedRequests int64
	var spend int64
	for _, c := range scan.Buckets {
		bucketRequests += c.Requests
		bucketInput += c.Input
		if c.SpendMicroUSD != nil {
			spend += *c.SpendMicroUSD
		}
	}
	for _, c := range scan.Keyed {
		if c.Key != domain.SourceChat && c.Key != domain.SourceTask {
			t.Fatalf("personal source %q, want only the admin's chat and task", c.Key)
		}
		keyedRequests += c.Requests
	}
	if bucketRequests != 2 || bucketInput != 110 || spend != 1100 || keyedRequests != 2 {
		t.Fatalf("personal scan = %d requests, %d input, spend %d, %d by source; want only the admin's 2, 110, 1100, 2",
			bucketRequests, bucketInput, spend, keyedRequests)
	}
	if lat := scan.Latency[usageHouse]; len(scan.Latency) != 1 || lat.P99 > 700 {
		t.Fatalf("personal latency = %+v, want only the admin's calls", scan.Latency)
	}
	if len(scan.Tasks) != 1 || scan.Tasks[0].Task != usageParent {
		t.Fatalf("personal tasks = %+v, want only the admin's task", scan.Tasks)
	}
}

func TestFeature_UPM220_TimeZoneBucketing(t *testing.T) {
	r := newUsageRig(t)
	// 23:30 UTC on the 17th is 00:30 on the 18th in Berlin.
	r.call(t, usageCall{at: time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC), user: usageAdmin, account: usageHouse, model: "claude-x", in: 1, cost: micro(1)})
	// 22:50 UTC on the 18th is the 18th in Berlin; 23:10 UTC is the 19th.
	r.call(t, usageCall{at: time.Date(2026, 11, 18, 22, 50, 0, 0, time.UTC), user: usageAdmin, account: usageHouse, model: "claude-x", in: 2, cost: micro(2)})
	r.call(t, usageCall{at: time.Date(2026, 11, 18, 23, 10, 0, 0, time.UTC), user: usageAdmin, account: usageHouse, model: "claude-x", in: 4, cost: micro(4)})

	week := personal(t, r.service(), usageAdmin, domain.UsageWeek)
	if week.TimeZone != "Europe/Berlin" || week.Bucket != domain.BucketDay {
		t.Fatalf("zone = %q bucket = %q", week.TimeZone, week.Bucket)
	}
	if b := bucketAt(t, week.Buckets, "2026-11-18"); b.Input != 3 {
		t.Fatalf("18th = %d input, want the 23:30 UTC call of the 17th and the 22:50 call", b.Input)
	}
	if b := bucketAt(t, week.Buckets, "2026-11-19"); b.Input != 4 {
		t.Fatalf("19th = %d input, want the 23:10 UTC call", b.Input)
	}

	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	w := domain.UsageWindowFor(domain.UsageToday, usageNow, berlin)
	if !w.Start.Equal(time.Date(2026, 11, 18, 23, 0, 0, 0, time.UTC)) || !w.End.Equal(time.Date(2026, 11, 19, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("today = [%s, %s), want local midnight to local midnight", w.Start, w.End)
	}
	today := personal(t, r.service(), usageAdmin, domain.UsageToday)
	if today.Bucket != domain.BucketHour || len(today.Buckets) != 1 {
		t.Fatalf("today = %s %+v, want one hour bucket", today.Bucket, today.Buckets)
	}
	if b := today.Buckets[0]; b.At != "2026-11-19T00:00:00+01:00" || b.Input != 4 {
		t.Fatalf("today bucket = %s %d, want local midnight with the 23:10 UTC call", b.At, b.Input)
	}
	body, err := json.Marshal(today)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"range":"today"`) {
		t.Fatalf("today body = %s, want range today", body)
	}
	if body, _ := json.Marshal(week); !strings.Contains(string(body), `"range":7,`) {
		t.Fatalf("week body = %s, want range 7", body)
	}
}

func TestFeature_UPM220_UnknownCostIsNullNeverZero(t *testing.T) {
	r := newUsageRig(t)
	d1 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	r.call(t, usageCall{at: d1, user: usageAdmin, account: usageHouse, model: "claude-x", in: 10, cost: micro(300)})
	r.call(t, usageCall{at: d1, user: usageAdmin, account: usageHouse, model: "mystery", in: 10, cost: micro(200), estimated: true})
	r.call(t, usageCall{at: d2, user: usageAdmin, account: usageHouse, model: "mystery", in: 10})

	got := personal(t, r.service(), usageAdmin, domain.UsageWeek)
	if !got.Estimated || got.UnpricedCalls != 2 || !moneyIs(got.SpendMicroUSD, 500) {
		t.Fatalf("totals = estimated %v, unpriced %d, spend %v; want true, 2, 500", got.Estimated, got.UnpricedCalls, got.SpendMicroUSD)
	}
	if b := bucketAt(t, got.Buckets, "2026-11-17"); !b.Estimated || !moneyIs(b.SpendMicroUSD, 500) {
		t.Fatalf("17th = %+v, want estimated 500", b)
	}
	unknown := bucketAt(t, got.Buckets, "2026-11-18")
	if unknown.SpendMicroUSD != nil || unknown.Estimated {
		t.Fatalf("18th = %+v, want null spend for a bucket with no priced call", unknown)
	}
	body, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"spend_micro_usd":null`) || strings.Contains(string(body), `"spend_micro_usd":0`) {
		t.Fatalf("18th body = %s, want spend null, never 0", body)
	}
}

func TestFeature_UPM220_SubscriptionAndLocalNotBilled(t *testing.T) {
	r := newUsageRig(t)
	at := usageNow.Add(-2 * time.Hour)
	// 1M input and 100k output on claude-x at 3 and 15 USD: 3 + 1.5 USD.
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageAdminSub, model: "claude-x", plan: domain.PlanSubscription, in: 1_000_000, out: 100_000, list: micro(4_500_000)})
	// 2M input on llama at the fallback 0.5 USD: 1 USD.
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageAdminLocal, model: "llama", plan: domain.PlanLocal, in: 2_000_000, list: micro(1_000_000)})
	// llama has no cache read rate, so this call has no list price.
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageAdminLocal, model: "llama", plan: domain.PlanLocal, in: 5, cr: 5})

	got := personal(t, r.service(), usageAdmin, domain.UsageWeek)
	if got.SpendMicroUSD != nil || got.Estimated || got.UnpricedCalls != 0 {
		t.Fatalf("spend = %v estimated %v unpriced %d, want null spend for calls that are not billed", got.SpendMicroUSD, got.Estimated, got.UnpricedCalls)
	}
	if !moneyIs(got.ListPriceMicroUSD, 5_500_000) {
		t.Fatalf("list price = %v, want 5500000 at the catalog rates", got.ListPriceMicroUSD)
	}
	for _, p := range got.Providers {
		if p.SpendMicroUSD != nil {
			t.Fatalf("provider %s spend = %d, want null", p.Name, *p.SpendMicroUSD)
		}
		if p.Scope != "own" {
			t.Fatalf("provider %s scope = %s, want own", p.Name, p.Scope)
		}
	}
	if b := got.Buckets[0]; b.SpendMicroUSD != nil || !moneyIs(b.ListPriceMicroUSD, 5_500_000) {
		t.Fatalf("bucket = %+v, want null spend and the list price", b)
	}
}

func TestFeature_UPM220_OwnKeyCallsShown(t *testing.T) {
	r := newUsageRig(t)
	at := usageNow.Add(-time.Hour)
	r.call(t, usageCall{at: at, user: usageMember, account: usageMemberOwn, model: "gpt-own", in: 40, out: 2, cost: micro(900), latency: ms(1200)})
	r.call(t, usageCall{at: at, user: usageMember, account: usageHouse, model: "claude-x", in: 10, cost: micro(100), charged: true, latency: ms(800)})

	got := personal(t, r.service(), usageMember, domain.UsageWeek)
	var own *domain.UsageProvider
	for i, p := range got.Providers {
		if p.ID == usageMemberOwn {
			own = &got.Providers[i]
		}
	}
	if own == nil {
		t.Fatalf("providers = %+v, want the member's own key", got.Providers)
	}
	if own.Name != "Member own key" || own.Scope != "own" || own.Requests != 1 || !moneyIs(own.SpendMicroUSD, 900) {
		t.Fatalf("own provider = %+v", *own)
	}
	if own.LatencyMS.P50 != 1200 || own.LatencyMS.P99 != 1200 {
		t.Fatalf("own latency = %+v, want 1200", own.LatencyMS)
	}
	if !moneyIs(got.SpendMicroUSD, 1000) {
		t.Fatalf("spend = %v, want own-key and house calls together", got.SpendMicroUSD)
	}
}

func TestFeature_UPM220_WorkspaceNeedsUsageView(t *testing.T) {
	r := newUsageRig(t)
	at := usageNow.Add(-time.Hour)
	r.call(t, usageCall{at: at, user: usageMember, account: usageHouse, model: "claude-x", in: 10, cost: micro(100)})
	r.call(t, usageCall{at: at, account: usageHouse, model: "claude-x", in: 20, cost: micro(200)})

	rec := r.get(t, usageMember, "/api/usage/workspace?range=7")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "claude-x") {
		t.Fatalf("member body = %s, want no usage", rec.Body.String())
	}
	rows := r.auditRows(t)
	if len(rows) != 1 || rows[0].action != string(audit.UsageWorkspaceView) || rows[0].outcome != string(audit.Denied) || rows[0].actor != usageMember {
		t.Fatalf("audit = %+v, want one denied usage.workspace.view by the member", rows)
	}

	rec = r.get(t, usageAdmin, "/api/usage/workspace?range=7&group=person")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache = %q", rec.Header().Get("Cache-Control"))
	}
	var got domain.WorkspaceUsageReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	names := map[string]int64{}
	for _, row := range got.Rows {
		names[row.Name] = row.Requests
	}
	if names[domain.SystemName] != 1 || names["=SUM(1,1)"] != 1 || !moneyIs(got.SpendMicroUSD, 300) {
		t.Fatalf("rows = %+v spend %v, want the member and System with 300 in all", got.Rows, got.SpendMicroUSD)
	}
	rows = r.auditRows(t)
	if len(rows) != 2 || rows[1].outcome != string(audit.Success) || rows[1].actor != usageAdmin {
		t.Fatalf("audit = %+v, want the admin's read sealed", rows)
	}
	values, _ := rows[1].changes["values"].(map[string]any)
	for field, want := range map[string]string{"range": "7", "group": "person", "format": "json"} {
		change, _ := values[field].(map[string]any)
		if change["to"] != want {
			t.Fatalf("audit %s = %v, want %s", field, values[field], want)
		}
	}

	if rec := r.get(t, usageAdmin, "/api/usage"); rec.Code != http.StatusOK {
		t.Fatalf("own usage = %d", rec.Code)
	}
	if rec := r.get(t, usageAdmin, "/api/usage?range=14"); rec.Code != http.StatusBadRequest {
		t.Fatalf("range 14 = %d, want 400", rec.Code)
	}
	if rows := r.auditRows(t); len(rows) != 2 {
		t.Fatalf("audit = %+v, want personal reads unaudited", rows)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/usage/workspace", nil)
	req.Header.Set("Authorization", "Bearer upmo_token")
	rec = httptest.NewRecorder()
	r.served(usageAdmin).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bearer = %d, want 403", rec.Code)
	}
}

func TestFeature_UPM220_WorkspaceRLSWithoutUsageView(t *testing.T) {
	r := newUsageRig(t)
	at := usageNow.Add(-time.Hour)
	r.call(t, usageCall{at: at, user: usageMember, account: usageHouse, model: "claude-x", in: 10, cost: micro(100)})
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", in: 20, cost: micro(200)})
	r.call(t, usageCall{at: at, account: usageHouse, model: "claude-x", in: 30, cost: micro(300)})

	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	w := domain.UsageWindowFor(domain.UsageWeek, usageNow, zone)
	scan, err := postgres.Usage{Pool: r.app}.WorkspaceUsage(context.Background(), app.Caller{User: usageMember}, w, domain.GroupPerson)
	if err != nil {
		t.Fatal(err)
	}
	var requests int64
	for _, c := range scan.Keyed {
		if c.Key != usageMember {
			t.Fatalf("row %q leaked to a reader without usage:view", c.Key)
		}
		requests += c.Requests
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want the member's own call only", requests)
	}
}

// TestFeature_UPM220_LedgerPolicyScopesRows reads the ledger and usage_daily
// straight through their row policies, with no user filter in the query.
func TestFeature_UPM220_LedgerPolicyScopesRows(t *testing.T) {
	ctx := context.Background()
	r := newUsageRig(t)
	// second is another ordinary person, so a member's own rows are told
	// apart from everyone else's among three people.
	const second = "019a0000-0000-7000-8000-000000000222"
	at := usageNow.Add(-time.Hour)
	r.call(t, usageCall{at: at, user: usageMember, account: usageHouse, model: "claude-x", in: 10, cost: micro(100)})
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", in: 20, cost: micro(200)})
	r.call(t, usageCall{at: at, user: second, account: usageHouse, model: "claude-x", in: 40, cost: micro(400)})
	r.call(t, usageCall{at: at, account: usageHouse, model: "claude-x", in: 30, cost: micro(300)})
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.usage_daily (day, user_id, model, plan_kind, source, calls)
		VALUES ('2026-11-18', $1::uuid, 'claude-x', 'metered', 'chat', 1),
			('2026-11-18', $2::uuid, 'claude-x', 'metered', 'chat', 1),
			('2026-11-18', $3::uuid, 'claude-x', 'metered', 'chat', 1),
			('2026-11-18', NULL, 'claude-x', 'metered', 'system', 1)`, usageMember, usageAdmin, second); err != nil {
		t.Fatal(err)
	}

	// owners lists the user ids a query returns, "system" for no person.
	owners := func(q pgx.Tx, table string) []string {
		t.Helper()
		var ids []string
		if err := q.QueryRow(ctx, `SELECT COALESCE(array_agg(COALESCE(user_id::text, 'system') ORDER BY COALESCE(user_id::text, 'system')), '{}') FROM gateway.`+table).Scan(&ids); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	// read runs in its own transaction on pool; set=false leaves both
	// settings unset.
	read := func(pool *pgxpool.Pool, set bool, user, allow string) (ledger, daily []string) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if set {
			if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`, user, allow); err != nil {
				t.Fatal(err)
			}
		}
		return owners(tx, "ledger"), owners(tx, "usage_daily")
	}

	all := []string{usageAdmin, usageMember, second, "system"}
	for _, c := range []struct {
		name        string
		pool        *pgxpool.Pool
		set         bool
		user, allow string
		want        []string
	}{
		{"member sees own rows", r.app, true, usageMember, "", []string{usageMember}},
		{"second member sees own rows", r.app, true, second, "", []string{second}},
		{"other permissions do not widen", r.app, true, usageMember, "settings:manage,agents:manage", []string{usageMember}},
		{"usage:view as a substring does not widen", r.app, true, usageMember, "usage:viewer", []string{usageMember}},
		{"usage:view sees all", r.app, true, usageAdmin, "settings:manage,usage:view", all},
		{"usage:view without a user sees all", r.app, true, "", "usage:view", all},
		{"owner sees all", r.owner, false, "", "", all},
		{"no settings sees nothing", r.app, false, "", "", nil},
		{"empty settings see nothing", r.app, true, "", "", nil},
	} {
		ledger, daily := read(c.pool, c.set, c.user, c.allow)
		if strings.Join(ledger, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: ledger rows of %v, want %v", c.name, ledger, c.want)
		}
		if strings.Join(daily, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: usage_daily rows of %v, want %v", c.name, daily, c.want)
		}
	}

	for _, stmt := range []string{
		`INSERT INTO gateway.ledger (at, route, link_position, model, plan_kind, failure_class, user_id, source)
			VALUES (now(), 'usage', 1, 'claude-x', 'metered', '', '` + usageAdmin + `', 'chat')`,
		`UPDATE gateway.ledger SET input_tokens = 0`,
		`DELETE FROM gateway.ledger`,
		`INSERT INTO gateway.usage_daily (day, user_id, model, plan_kind, source) VALUES ('2026-11-17', '` + usageAdmin + `', 'claude-x', 'metered', 'chat')`,
		`UPDATE gateway.usage_daily SET calls = 0`,
		`DELETE FROM gateway.usage_daily`,
	} {
		tx, err := r.app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', 'usage:view,settings:manage', true)`, usageAdmin); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, stmt)
		_ = tx.Rollback(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: err = %v, want insufficient_privilege", stmt, err)
		}
	}
	if n := countLedger(t, r.owner); n != 4 {
		t.Fatalf("ledger rows after the app's writes = %d, want the 4 seeded", n)
	}
}

func countLedger(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.ledger`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFeature_UPM220_GroupingsAndCSV(t *testing.T) {
	r := newUsageRig(t)
	at := usageNow.Add(-26 * time.Hour)
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", agent: usageAgent, team: usageTeam, in: 100, out: 10, rs: 3, cost: micro(1000)})
	r.call(t, usageCall{at: at, user: usageMember, account: usageMemberOwn, model: "gpt-own", source: domain.SourceTask, run: usageOther, in: 50, cr: 5, cost: micro(400), estimated: true})
	r.call(t, usageCall{at: usageNow, user: usageAdmin, account: usageAdminSub, model: "claude-x", plan: domain.PlanSubscription, in: 1_000_000, list: micro(3_000_000)})
	r.call(t, usageCall{at: usageNow, account: usageHouse, model: "mystery", in: 7, failure: "transient"})

	s := r.service()
	ctx := context.Background()
	for _, group := range []domain.UsageGroup{domain.GroupPerson, domain.GroupTeam, domain.GroupAgent, domain.GroupModel, domain.GroupProvider, domain.GroupSource} {
		got, err := s.Workspace(ctx, usageAdmin, domain.UsageWeek, group, "json")
		if err != nil {
			t.Fatal(err)
		}
		var sum domain.UsageTotals
		var spend, list int64
		for _, row := range got.Rows {
			sum.Requests += row.Requests
			sum.Errors += row.Errors
			sum.Tokens += row.Tokens
			sum.UnpricedCalls += row.UnpricedCalls
			if row.SpendMicroUSD != nil {
				spend += *row.SpendMicroUSD
			}
			if row.ListPriceMicroUSD != nil {
				list += *row.ListPriceMicroUSD
			}
		}
		var tokens, requests int64
		for _, b := range got.Buckets {
			requests += b.Requests
			tokens += b.Input + b.Output + b.CacheRead + b.CacheWrite
		}
		if sum.Requests != 4 || requests != 4 || sum.Errors != 1 || sum.Tokens != tokens || sum.UnpricedCalls != got.UnpricedCalls ||
			!moneyIs(got.SpendMicroUSD, spend) || spend != 1400 || !moneyIs(got.ListPriceMicroUSD, list) || list != 3_000_000 {
			t.Fatalf("%s rows = %+v, totals spend %v list %v unpriced %d; want rows that add up to the workspace", group, got.Rows, got.SpendMicroUSD, got.ListPriceMicroUSD, got.UnpricedCalls)
		}
		names := map[string]string{}
		for _, row := range got.Rows {
			names[row.ID] = row.Name
		}
		switch group {
		case domain.GroupTeam:
			if names[usageTeam] != "Finance" || names[""] != domain.NoTeamName {
				t.Fatalf("team names = %v", names)
			}
		case domain.GroupAgent:
			if names[usageAgent] != domain.PrivateAgentName || names[""] != domain.NoAgentName {
				t.Fatalf("agent names = %v", names)
			}
		case domain.GroupProvider:
			// The member's private account is hidden from the admin by row
			// security, so it keeps its id and loses its name.
			if names[usageMemberOwn] != domain.PrivateProviderName || names[usageHouse] != "Anthropic" || names[usageAdminSub] != "Admin Claude Max" {
				t.Fatalf("provider names = %v", names)
			}
		}
	}

	rec := r.get(t, usageAdmin, "/api/usage/workspace?range=30&group=person&format=csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("csv = %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="usage-workspace-person-30.csv"` {
		t.Fatalf("disposition = %q", cd)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache = %q", rec.Header().Get("Cache-Control"))
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || len(records[0]) != 17 || strings.Join(records[0], ",") != "range,time_zone,group,group_id,group_name,requests,errors,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,total_tokens,spend_micro_usd,estimated,unpriced_calls,list_price_micro_usd" {
		t.Fatalf("csv = %v", records)
	}
	byName := map[string][]string{}
	for _, rec := range records[1:] {
		if len(rec) != 17 {
			t.Fatalf("csv row = %v", rec)
		}
		byName[rec[4]] = rec
	}
	if _, ok := byName["=SUM(1,1)"]; ok {
		t.Fatalf("csv = %v, want the formula name neutralised", records)
	}
	member := byName["'=SUM(1,1)"]
	if member == nil || member[0] != "30" || member[1] != "Europe/Berlin" || member[13] != "400" || member[14] != "true" || member[15] != "1" {
		t.Fatalf("member row = %v", member)
	}
	if system := byName[domain.SystemName]; system == nil || system[13] != "" || system[15] != "1" {
		t.Fatalf("system row = %v, want an empty unknown spend cell", system)
	}
	if admin := byName["Ada Admin"]; admin == nil || admin[11] != "3" || admin[12] != "1000110" || admin[16] != "3000000" {
		t.Fatalf("admin row = %v", admin)
	}
	rows := r.auditRows(t)
	last := rows[len(rows)-1]
	values, _ := last.changes["values"].(map[string]any)
	if format, _ := values["format"].(map[string]any); last.outcome != string(audit.Success) || format["to"] != "csv" {
		t.Fatalf("audit = %+v, want the csv export sealed", last)
	}
}

func TestFeature_UPM220_CSVFormulaSafety(t *testing.T) {
	r := newUsageRig(t)
	at := usageNow.Add(-time.Hour)
	// A model id is both the group_id and the group_name of the model group.
	risky := []string{"=HYPERLINK(1)", "+1", "-1", "@SUM(1)", "\tcmd", "\rcmd"}
	awkward := "Acme, \"Pro\"\nmodel"
	for _, model := range append(risky, awkward) {
		r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: model, in: 1, cost: micro(1)})
	}

	rec := r.get(t, usageAdmin, "/api/usage/workspace?range=7&group=model&format=csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("csv = %d %s", rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("csv does not parse back: %v", err)
	}
	if len(records) != 1+len(risky)+1 {
		t.Fatalf("csv = %q, want a header and one row per model", records)
	}
	byID := map[string][]string{}
	for _, row := range records[1:] {
		if len(row) != 17 {
			t.Fatalf("csv row = %q, want 17 cells", row)
		}
		byID[row[3]] = row
	}
	for _, model := range risky {
		row := byID["'"+model]
		if row == nil || row[4] != "'"+model {
			t.Fatalf("csv = %q, want %q neutralised in group_id and group_name", records, model)
		}
		if _, ok := byID[model]; ok {
			t.Fatalf("csv = %q, want no bare %q", records, model)
		}
	}
	if row := byID[awkward]; row == nil || row[4] != awkward || row[5] != "1" {
		t.Fatalf("csv = %q, want %q kept whole in one cell", records, awkward)
	}
}

// TestFeature_UPM220_DSTBuckets reads today on the two Berlin days whose
// length is not 24 hours, with one call in every UTC hour of the day.
func TestFeature_UPM220_DSTBuckets(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []struct {
		name     string
		now      time.Time
		hours    int
		first    string
		last     string
		adjacent [2]string
		absent   string
		extra    []time.Time
	}{
		{
			name: "fall back", now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), hours: 25,
			first: "2026-10-25T00:00:00+02:00", last: "2026-10-25T23:00:00+01:00",
			// 02:00 local happens twice, first in summer time, then in winter time.
			adjacent: [2]string{"2026-10-25T02:00:00+02:00", "2026-10-25T02:00:00+01:00"},
			extra: []time.Time{
				time.Date(2026, 10, 25, 0, 59, 0, 0, time.UTC), // 02:59 +02:00
				time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC),  // 02:00 +01:00
			},
		},
		{
			name: "spring forward", now: time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC), hours: 23,
			first: "2026-03-29T00:00:00+01:00", last: "2026-03-29T23:00:00+02:00",
			// 02:00 local never happens: 01:59 +01:00 is followed by 03:00 +02:00.
			adjacent: [2]string{"2026-03-29T01:00:00+01:00", "2026-03-29T03:00:00+02:00"},
			absent:   "2026-03-29T02:00",
			extra: []time.Time{
				time.Date(2026, 3, 29, 0, 59, 0, 0, time.UTC), // 01:59 +01:00
				time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC),  // 03:00 +02:00
			},
		},
	} {
		t.Run(day.name, func(t *testing.T) {
			r := newUsageRig(t)
			w := domain.UsageWindowFor(domain.UsageToday, day.now, berlin)
			if got := int(w.End.Sub(w.Start) / time.Hour); got != day.hours {
				t.Fatalf("today = [%s, %s), %d hours; want %d", w.Start, w.End, got, day.hours)
			}
			var input, spend int64
			seed := func(at time.Time, n int64) {
				r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", in: int(n), cost: micro(n)})
				input += n
				spend += n
			}
			for h := 0; h < day.hours; h++ {
				seed(w.Start.Add(time.Duration(h)*time.Hour+30*time.Minute), int64(h+1))
			}
			for i, at := range day.extra {
				seed(at, int64(1000*(i+1)))
			}

			s := r.service()
			s.Now = func() time.Time { return day.now }
			got := personal(t, s, usageAdmin, domain.UsageToday)
			if got.Bucket != domain.BucketHour || len(got.Buckets) != day.hours {
				t.Fatalf("buckets = %d %s, want %d hour buckets", len(got.Buckets), got.Bucket, day.hours)
			}
			seen := map[string]bool{}
			var sumInput, sumSpend int64
			for i, b := range got.Buckets {
				if seen[b.At] {
					t.Fatalf("bucket %s appears twice", b.At)
				}
				seen[b.At] = true
				if want := w.Start.Add(time.Duration(i) * time.Hour).In(berlin).Format(time.RFC3339); b.At != want {
					t.Fatalf("bucket %d = %s, want %s", i, b.At, want)
				}
				if day.absent != "" && strings.HasPrefix(b.At, day.absent) {
					t.Fatalf("bucket %s is in the skipped hour", b.At)
				}
				sumInput += b.Input
				if b.SpendMicroUSD != nil {
					sumSpend += *b.SpendMicroUSD
				}
			}
			if got.Buckets[0].At != day.first || got.Buckets[day.hours-1].At != day.last {
				t.Fatalf("buckets run %s to %s, want %s to %s", got.Buckets[0].At, got.Buckets[day.hours-1].At, day.first, day.last)
			}
			for i := 0; i+1 < len(got.Buckets); i++ {
				if got.Buckets[i].At == day.adjacent[0] && got.Buckets[i+1].At != day.adjacent[1] {
					t.Fatalf("after %s comes %s, want %s", day.adjacent[0], got.Buckets[i+1].At, day.adjacent[1])
				}
			}
			if !seen[day.adjacent[0]] || !seen[day.adjacent[1]] {
				t.Fatalf("buckets = %v, want %s and %s", seen, day.adjacent[0], day.adjacent[1])
			}
			// The extra calls sit either side of the clock change and stay in
			// their own hour.
			first, second := bucketAt(t, got.Buckets, day.adjacent[0]), bucketAt(t, got.Buckets, day.adjacent[1])
			if first.Input < 1000 || first.Input >= 2000 || second.Input < 2000 {
				t.Fatalf("%s = %d input, %s = %d input; want the extra calls split across them", first.At, first.Input, second.At, second.Input)
			}
			if sumInput != input || !moneyIs(got.SpendMicroUSD, spend) || sumSpend != spend {
				t.Fatalf("input %d spend %v (buckets %d), want %d and %d", sumInput, got.SpendMicroUSD, sumSpend, input, spend)
			}
		})
	}
}

func TestFeature_UPM220_TaskRollup(t *testing.T) {
	r := newUsageRig(t)
	at := usageNow.Add(-time.Hour)
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", source: domain.SourceTask, run: usageParent, in: 100, out: 10, cost: micro(1000)})
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", source: domain.SourceTask, run: usageChild, parent: usageParent, in: 50, out: 5, cr: 20, cost: micro(500)})
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "mystery", source: domain.SourceTask, run: usageChild, parent: usageParent, in: 9, cw: 1})
	r.call(t, usageCall{at: at, user: usageAdmin, account: usageAdminSub, model: "claude-x", plan: domain.PlanSubscription, source: domain.SourceTask, run: usageOther, in: 3})

	got := personal(t, r.service(), usageAdmin, domain.UsageWeek)
	if len(got.Tasks) != 2 {
		t.Fatalf("tasks = %+v, want the parent task and the other", got.Tasks)
	}
	task := got.Tasks[0]
	if task.TaskID != usageParent || task.Goal != "Daily finance digest" || !moneyIs(task.CostMicroUSD, 1500) {
		t.Fatalf("task = %+v, want the parent with its child's runs", task)
	}
	if task.Input != 159 || task.Output != 15 || task.CacheRead != 20 || task.CacheWrite != 1 || len(task.ByModel) != 2 {
		t.Fatalf("task tokens = %+v", task)
	}
	var in, out, cr, cw int64
	var cost int64
	for _, m := range task.ByModel {
		in += m.Input
		out += m.Output
		cr += m.CacheRead
		cw += m.CacheWrite
		if m.CostMicroUSD != nil {
			cost += *m.CostMicroUSD
		}
	}
	if in != task.Input || out != task.Output || cr != task.CacheRead || cw != task.CacheWrite || cost != *task.CostMicroUSD {
		t.Fatalf("by model = %+v, want it to add up to the task", task.ByModel)
	}
	claude, mystery := task.ByModel[0], task.ByModel[1]
	if claude.ModelID != "claude-x" || !moneyIs(claude.CostMicroUSD, 1500) || claude.Input != 150 || mystery.ModelID != "mystery" || mystery.CostMicroUSD != nil {
		t.Fatalf("by model = %+v", task.ByModel)
	}
	other := got.Tasks[1]
	if other.TaskID != usageOther || other.CostMicroUSD != nil || other.Goal != "" {
		t.Fatalf("other task = %+v, want unknown cost for a subscription task", other)
	}
}
