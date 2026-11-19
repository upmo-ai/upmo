//go:build integration

package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	agentpg "github.com/upmo-ai/upmo/internal/agents/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

type probeReply struct {
	models []domain.SeenModel
	class  domain.FailureClass
	err    error
}

type scriptedProber struct {
	mu      sync.Mutex
	calls   []app.Attempt
	handler func(attempt app.Attempt) probeReply
}

func (p *scriptedProber) Probe(_ context.Context, attempt app.Attempt) ([]domain.SeenModel, domain.FailureClass, error) {
	p.mu.Lock()
	p.calls = append(p.calls, attempt)
	fn := p.handler
	p.mu.Unlock()
	if fn == nil {
		return nil, domain.ClassOK, nil
	}
	r := fn(attempt)
	return r.models, r.class, r.err
}

func (p *scriptedProber) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type checkRig struct {
	t           *testing.T
	owner       *pgxpool.Pool
	appPool     *pgxpool.Pool
	keys        Keyring
	svc         app.Providers
	store       postgres.Store
	prober      *scriptedProber
	logs        *syncBuf
	nowMu       sync.Mutex
	now         time.Time
	notifyMu    sync.Mutex
	notifyCalls int
	notifyErr   func(call int, userID, title string) error
	work        *jobs.Client
	deps        ProviderCheckDeps
	ownerUID    string
	adminUID    string
	memUID      string
	teamID      string
}

func (r *checkRig) currentNow() time.Time {
	r.nowMu.Lock()
	defer r.nowMu.Unlock()
	return r.now
}

func (r *checkRig) setNow(t time.Time) {
	r.nowMu.Lock()
	defer r.nowMu.Unlock()
	r.now = t
}

func (r *checkRig) setNotifyErr(fn func(call int, userID, title string) error) {
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()
	r.notifyErr = fn
}

func (r *checkRig) notify(ctx context.Context, tx pgx.Tx, userID, title string) error {
	r.notifyMu.Lock()
	r.notifyCalls++
	call := r.notifyCalls
	hook := r.notifyErr
	r.notifyMu.Unlock()
	if hook != nil {
		if err := hook(call, userID, title); err != nil {
			return err
		}
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return err
	}
	_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
	return err
}

func newCheckRig(t *testing.T) *checkRig {
	t.Helper()
	ctx := context.Background()
	owner := pgtest.New(t)
	migrateWithIdentity(t, owner)
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, notices.Schema, notices.Migrations()); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)

	var ownerRoleID, adminRoleID, memberRoleID string
	for key, dst := range map[string]*string{
		"owner":  &ownerRoleID,
		"admin":  &adminRoleID,
		"member": &memberRoleID,
	} {
		if err := owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = $1`, key).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}

	const (
		ownerUID  = "11111111-1111-4111-8111-111111111111"
		adminUID  = "22222222-2222-4222-8222-222222222222"
		memberUID = "33333333-3333-4333-8333-333333333333"
	)
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.users (id, email, name, role_id, joined_at)
		VALUES
		  ($1::uuid, 'owner@example.com', 'Owner', $4::uuid, now()),
		  ($2::uuid, 'admin@example.com', 'Admin', $5::uuid, now()),
		  ($3::uuid, 'member@example.com', 'Member', $6::uuid, now())`,
		ownerUID, adminUID, memberUID, ownerRoleID, adminRoleID, memberRoleID); err != nil {
		t.Fatal(err)
	}
	var teamID string
	if err := owner.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Core') RETURNING id::text`).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, teamID, memberUID); err != nil {
		t.Fatal(err)
	}

	keys := masterKeys(t, 'a')
	logs := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	prober := &scriptedProber{}
	fixedNow := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	svc := NewProviders(appPool, keys, logger)
	rig := &checkRig{
		t:        t,
		owner:    owner,
		appPool:  appPool,
		keys:     keys,
		svc:      svc,
		store:    postgres.Store{Pool: owner},
		prober:   prober,
		logs:     logs,
		now:      fixedNow,
		ownerUID: ownerUID,
		adminUID: adminUID,
		memUID:   memberUID,
		teamID:   teamID,
	}

	secrets := Chain{EnvSecrets{}, DBSecrets{Pool: owner, Keys: keys}}
	deps := ProviderCheckDeps{
		Owner:   owner,
		App:     appPool,
		Secrets: secrets,
		Probes: map[string]app.Prober{
			domain.AdapterOpenAI: prober,
			domain.AdapterFake:   prober,
		},
		AgentModelUses: func(ctx context.Context, provider string, models []string) ([]string, error) {
			return agentpg.SystemProviderModelUses(ctx, owner, provider, models)
		},
		AutomationModelUses: func(context.Context, string, []string) ([]string, error) {
			return nil, nil
		},
		ActiveUser: func(ctx context.Context, userID string) (bool, error) {
			return identity.ActiveUser(ctx, owner, userID)
		},
		SettingsManagers: func(ctx context.Context) ([]string, error) {
			return identity.SettingsManagers(ctx, owner)
		},
		Notify: rig.notify,
		Now:    rig.currentNow,
		Log:    logger,
	}

	workers := river.NewWorkers()
	AddProviderChecker(workers, deps)
	workClient, err := jobs.Open(appPool, jobs.WithWorkers(workers))
	if err != nil {
		t.Fatal(err)
	}
	if err := workClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = workClient.Stop(stopCtx)
	})
	deps.Enqueuer = workClient

	svc.Allow = func(_ context.Context, user string) (string, error) {
		if user == ownerUID || user == adminUID {
			return domain.SharePermission, nil
		}
		return "", nil
	}
	svc.Teams = func(_ context.Context, user string) ([]string, error) {
		if user == memberUID {
			return []string{teamID}, nil
		}
		return nil, nil
	}
	svc.LocalPlan = func(context.Context, string) bool { return true }
	svc.Probes = deps.Probes

	rig.work = workClient
	rig.deps = deps
	rig.svc = svc
	return rig
}

func (r *checkRig) runCheckJob(accountID string) {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.appPool.Begin(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := r.work.InsertTx(ctx, tx, ProviderCheckArgs{AccountID: accountID}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		r.t.Fatal(err)
	}
	r.waitJob(res.Job.ID)
}

func (r *checkRig) runDispatchJob() {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.appPool.Begin(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := r.work.InsertTx(ctx, tx, ProviderCheckDispatchArgs{}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		r.t.Fatal(err)
	}
	r.waitJob(res.Job.ID)
}

func (r *checkRig) waitJob(jobID int64) {
	r.t.Helper()
	r.waitJobState(jobID, false, 10*time.Second)
}

func (r *checkRig) waitJobEventuallyCompleted(jobID int64) {
	r.t.Helper()
	r.waitJobState(jobID, true, 15*time.Second)
}

func (r *checkRig) waitJobState(jobID int64, allowRetry bool, timeout time.Duration) {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var state string
		var errorsText string
		err := r.owner.QueryRow(context.Background(),
			`SELECT state::text, COALESCE(errors::text, '[]') FROM platform.river_job WHERE id = $1`, jobID).
			Scan(&state, &errorsText)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			r.t.Fatal(err)
		}
		if err == nil {
			switch state {
			case "completed":
				return
			case "discarded", "cancelled":
				r.t.Fatalf("job %d ended in state %s: %s", jobID, state, errorsText)
			case "retryable":
				if !allowRetry {
					r.t.Fatalf("job %d failed to retryable: %s", jobID, errorsText)
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for river job %d", jobID)
}

func (r *checkRig) noticesFor(userID string) []notices.Notice {
	r.t.Helper()
	uid, err := uuid.Parse(userID)
	if err != nil {
		r.t.Fatal(err)
	}
	list, err := (notices.Store{Pool: r.appPool}).List(context.Background(), uid, 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return list
}

func (r *checkRig) addAccount(userID string, in app.NewProvider) domain.Provider {
	r.t.Helper()
	made, err := r.svc.Add(context.Background(), userID, in)
	if err != nil {
		r.t.Fatal(err)
	}
	return made
}

func (r *checkRig) getAccount(userID, accountID string) domain.Provider {
	r.t.Helper()
	list, err := r.svc.List(context.Background(), userID)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, a := range list {
		if a.ID == accountID {
			return a
		}
	}
	r.t.Fatalf("account %s not found for user %s", accountID, userID)
	return domain.Provider{}
}

func TestFeature_UPM364_DispatchSpreadsOverDayAndSkipsDisabled(t *testing.T) {
	r := newCheckRig(t)
	ctx := context.Background()

	acc1 := r.addAccount(r.ownerUID, app.NewProvider{
		Provider: "openai", Name: "Enabled One", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: adaKey,
		Models: []string{"gpt-4o"},
	})
	acc2 := r.addAccount(r.ownerUID, app.NewProvider{
		Provider: "openai", Name: "Enabled Two", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: newKey,
		Models: []string{"gpt-4o-mini"},
	})
	highAcc := r.addAccount(r.ownerUID, app.NewProvider{
		Provider: "openai", Name: "High Offset", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: newKey,
	})
	var highID string
	for i := 0; ; i++ {
		cand := uuid.NewSHA1(uuid.NameSpaceOID, fmt.Appendf(nil, "upm364-high-offset-%d", i)).String()
		if domain.AccountCheckOffset(cand) >= 23*time.Hour+30*time.Minute {
			highID = cand
			break
		}
	}
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET id = $2::uuid WHERE id = $1::uuid`, highAcc.ID, highID); err != nil {
		t.Fatal(err)
	}
	highAcc.ID = highID

	accOff := r.addAccount(r.ownerUID, app.NewProvider{
		Provider: "openai", Name: "Disabled Account", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: adaKey,
		Models: []string{"gpt-4o"},
	})
	off := false
	if _, err := r.svc.Change(ctx, r.ownerUID, accOff.ID, app.ProviderChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}

	// Dispatch at midnight UTC two days in the future so scheduled check jobs
	// never auto-fire while we simulate same-day and >23h restarts.
	baseDay := time.Now().UTC().Truncate(24 * time.Hour).Add(48 * time.Hour)
	day1 := baseDay.Format("2006-01-02")
	day2 := baseDay.AddDate(0, 0, 1).Format("2006-01-02")

	r.setNow(baseDay)
	r.runDispatchJob()

	type scheduledJob struct {
		day string
		at  time.Time
	}
	readJobs := func() map[string][]scheduledJob {
		rows, err := r.owner.Query(ctx, `
			SELECT args->>'account_id', args->>'day', scheduled_at
			FROM platform.river_job
			WHERE kind = 'gateway_provider_check'
			ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][]scheduledJob{}
		for rows.Next() {
			var id, day string
			var at time.Time
			if err := rows.Scan(&id, &day, &at); err != nil {
				t.Fatal(err)
			}
			out[id] = append(out[id], scheduledJob{day: day, at: at.UTC()})
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	scheduled := readJobs()
	if len(scheduled) != 3 {
		t.Fatalf("scheduled accounts = %d (%v), want 3 (disabled account skipped)", len(scheduled), scheduled)
	}
	if _, ok := scheduled[accOff.ID]; ok {
		t.Fatal("disabled account was scheduled on dispatch")
	}
	for _, id := range []string{acc1.ID, acc2.ID, highAcc.ID} {
		jobs := scheduled[id]
		if len(jobs) != 1 {
			t.Fatalf("account %s initial jobs = %d, want 1", id, len(jobs))
		}
		if jobs[0].day != day1 {
			t.Fatalf("account %s day = %q, want %q", id, jobs[0].day, day1)
		}
		want := baseDay.Add(domain.AccountCheckOffset(id))
		if jobs[0].at.Sub(want).Abs() > time.Millisecond {
			t.Fatalf("account %s scheduled_at = %v, want %v", id, jobs[0].at, want)
		}
	}
	if scheduled[acc1.ID][0].at.Equal(scheduled[acc2.ID][0].at) {
		t.Fatalf("expected spread offsets for %s and %s, both at %v", acc1.ID, acc2.ID, scheduled[acc1.ID][0].at)
	}

	// Restart later the same day (12h) and after 23h (23h15m, while highAcc's >=23h30m slot is still ahead).
	r.setNow(baseDay.Add(12 * time.Hour))
	r.runDispatchJob()
	r.setNow(baseDay.Add(23*time.Hour + 15*time.Minute))
	r.runDispatchJob()

	afterRestarts := readJobs()
	if got := afterRestarts[highAcc.ID]; len(got) != 1 || got[0].day != day1 {
		t.Fatalf("high-offset account jobs after same-day and >23h restarts = %+v, want single job on %s", got, day1)
	}
	for _, id := range []string{acc1.ID, acc2.ID, highAcc.ID} {
		seenDays := map[string]int{}
		for _, j := range afterRestarts[id] {
			seenDays[j.day]++
			seenDays["utc:"+j.at.Format("2006-01-02")]++
		}
		for k, count := range seenDays {
			if count != 1 {
				t.Fatalf("account %s had %d jobs for %s (%+v), want 1", id, count, k, afterRestarts[id])
			}
		}
	}

	// Advancing into the next day after highAcc's slot passes schedules day2 once.
	r.setNow(baseDay.Add(24*time.Hour + 5*time.Minute))
	r.runDispatchJob()
	nextDay := readJobs()
	if got := nextDay[highAcc.ID]; len(got) != 2 || got[0].day != day1 || got[1].day != day2 {
		t.Fatalf("high-offset account jobs on next day = %+v, want [%s, %s]", got, day1, day2)
	}

	// Executing a check job directly for a disabled account also skips probing.
	r.runCheckJob(accOff.ID)
	if r.prober.callCount() != 0 {
		t.Fatalf("disabled account triggered %d probe calls, want 0", r.prober.callCount())
	}
}

func TestFeature_UPM364_KeyRejectedFlipsStatusAndNotifiesOnceUntilRecovered(t *testing.T) {
	r := newCheckRig(t)
	ctx := context.Background()

	acct := r.addAccount(r.memUID, app.NewProvider{
		Provider: "openai", Name: "Ada OpenAI", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: adaKey,
		Models: []string{"gpt-4o"},
	})

	var nextReply probeReply
	r.prober.handler = func(app.Attempt) probeReply { return nextReply }

	// 1. First rejected check flips status to Key rejected and sends 1 notice to owner only.
	nextReply = probeReply{class: domain.ClassCredential, err: errors.New("401 unauthorized")}
	r.runCheckJob(acct.ID)

	got := r.getAccount(r.memUID, acct.ID)
	if got.Status != domain.StatusKeyRejected {
		t.Fatalf("status = %q, want %q", got.Status, domain.StatusKeyRejected)
	}
	memNotices := r.noticesFor(r.memUID)
	if len(memNotices) != 1 || memNotices[0].Kind != notices.KindFailed || !strings.Contains(memNotices[0].Title, "Ada OpenAI") {
		t.Fatalf("member notices = %+v, want 1 failed notice naming Ada OpenAI", memNotices)
	}
	if adminNotices := r.noticesFor(r.adminUID); len(adminNotices) != 0 {
		t.Fatalf("admin got %d notices for private account, want 0", len(adminNotices))
	}

	// 2. Repeat daily check with rejected key sends 0 new notices.
	r.runCheckJob(acct.ID)
	if n := len(r.noticesFor(r.memUID)); n != 1 {
		t.Fatalf("notices after repeat failure = %d, want 1", n)
	}

	// Verify upmo_app cannot write notified_causes directly.
	if _, err := r.appPool.Exec(ctx, `UPDATE gateway.provider_accounts SET notified_causes = '{}' WHERE id = $1::uuid`, acct.ID); err == nil {
		t.Fatal("upmo_app unexpectedly allowed to update notified_causes")
	}

	// 3. Successful check clears key_rejected and sets Connected.
	nextReply = probeReply{class: domain.ClassOK, models: []domain.SeenModel{{ID: "gpt-4o"}}}
	r.runCheckJob(acct.ID)

	got = r.getAccount(r.memUID, acct.ID)
	if got.Status != domain.StatusConnected {
		t.Fatalf("status after recovery = %q, want %q", got.Status, domain.StatusConnected)
	}

	// 4. Subsequent key rejection sends a 2nd notice.
	nextReply = probeReply{class: domain.ClassCredential, err: errors.New("401 invalid key")}
	r.runCheckJob(acct.ID)
	if n := len(r.noticesFor(r.memUID)); n != 2 {
		t.Fatalf("notices after post-recovery rejection = %d, want 2", n)
	}

	// 5. Disabled or unjoined private account owner receives no notice.
	nextReply = probeReply{class: domain.ClassOK, models: []domain.SeenModel{{ID: "gpt-4o"}}}
	r.runCheckJob(acct.ID)
	if _, err := r.owner.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1::uuid`, r.memUID); err != nil {
		t.Fatal(err)
	}
	nextReply = probeReply{class: domain.ClassCredential, err: errors.New("401 disabled owner")}
	r.runCheckJob(acct.ID)
	got = r.getAccount(r.memUID, acct.ID)
	if got.Status != domain.StatusKeyRejected {
		t.Fatalf("status with disabled owner = %q, want %q", got.Status, domain.StatusKeyRejected)
	}
	if n := len(r.noticesFor(r.memUID)); n != 2 {
		t.Fatalf("disabled private owner got %d notices, want 2 (no new notice)", n)
	}

	var memberRoleID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRoleID); err != nil {
		t.Fatal(err)
	}
	const unjoinedMemberUID = "66666666-6666-4666-8666-666666666666"
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO identity.users (id, email, name, role_id, joined_at, disabled_at)
		VALUES ($1::uuid, 'unjoined-owner@example.com', 'Unjoined Owner', $2::uuid, NULL, NULL)`,
		unjoinedMemberUID, memberRoleID); err != nil {
		t.Fatal(err)
	}
	unjoinedAcct := r.addAccount(unjoinedMemberUID, app.NewProvider{
		Provider: "openai", Name: "Unjoined OpenAI", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: newKey,
		Models: []string{"gpt-4o"},
	})
	r.runCheckJob(unjoinedAcct.ID)
	gotUnjoined := r.getAccount(unjoinedMemberUID, unjoinedAcct.ID)
	if gotUnjoined.Status != domain.StatusKeyRejected {
		t.Fatalf("status with unjoined owner = %q, want %q", gotUnjoined.Status, domain.StatusKeyRejected)
	}
	if n := len(r.noticesFor(unjoinedMemberUID)); n != 0 {
		t.Fatalf("unjoined private owner got %d notices, want 0", n)
	}
}

func TestFeature_UPM364_TwoMissesRetireModelAndNotifyInUseRecipients(t *testing.T) {
	r := newCheckRig(t)
	ctx := context.Background()

	// Add a disabled admin and an unjoined admin to verify only active settings:manage users are notified.
	var adminRoleID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'admin'`).Scan(&adminRoleID); err != nil {
		t.Fatal(err)
	}
	const (
		disabledAdminUID = "44444444-4444-4444-8444-444444444444"
		unjoinedAdminUID = "55555555-5555-4555-8555-555555555555"
	)
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO identity.users (id, email, name, role_id, joined_at, disabled_at)
		VALUES
		  ($1::uuid, 'disabled@example.com', 'Disabled', $3::uuid, now(), now()),
		  ($2::uuid, 'unjoined@example.com', 'Unjoined', $3::uuid, NULL, NULL)`,
		disabledAdminUID, unjoinedAdminUID, adminRoleID); err != nil {
		t.Fatal(err)
	}

	acct := r.addAccount(r.ownerUID, app.NewProvider{
		Provider: "openai", Name: "Workspace OpenAI", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: adaKey,
		Models:     []string{"m-route", "m-agent", "m-unused", "m-keep"},
		Visibility: domain.VisibilityEveryone,
	})

	// Seed a route using m-route + fallback m-keep, and an agent using m-agent.
	var routeID string
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO gateway.routes (name, capability, installed)
		VALUES ('retire-route', 'chat', true)
		RETURNING id::text`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
		VALUES ($1::uuid, 1, $2::uuid, 'm-route'), ($1::uuid, 2, $2::uuid, 'm-keep')`, routeID, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO agents.agents (owner_user_id, visibility, team_ids, name, slug, default_model_provider, default_model_id)
		VALUES ($1::uuid, 'everyone', '{}'::uuid[], 'Agent One', 'agent-one', 'openai', 'm-agent')`,
		r.memUID); err != nil {
		t.Fatal(err)
	}

	var discovered []domain.SeenModel
	r.prober.handler = func(app.Attempt) probeReply {
		return probeReply{models: discovered, class: domain.ClassOK}
	}

	// Initial successful check discovers all models.
	discovered = []domain.SeenModel{{ID: "m-route"}, {ID: "m-agent"}, {ID: "m-unused"}, {ID: "m-keep"}}
	r.runCheckJob(acct.ID)

	// 1st miss: m-route, m-agent, m-unused vanish once -> misses = 1, retired = false, still usable, 0 notices.
	discovered = []domain.SeenModel{{ID: "m-keep"}}
	r.runCheckJob(acct.ID)

	var misses int
	var retired bool
	if err := r.owner.QueryRow(ctx, `SELECT misses, retired FROM gateway.provider_models WHERE account_id = $1::uuid AND model = 'm-route'`, acct.ID).
		Scan(&misses, &retired); err != nil {
		t.Fatal(err)
	}
	if misses != 1 || retired {
		t.Fatalf("after 1st miss: misses=%d retired=%v, want misses=1 retired=false", misses, retired)
	}
	if n := len(r.noticesFor(r.ownerUID)); n != 0 {
		t.Fatalf("notices after 1st miss = %d, want 0", n)
	}

	// Providers.Update must preserve misses and retired for models staying on the account.
	keepModels := []string{"m-route", "m-agent", "m-unused", "m-keep"}
	if _, err := r.svc.Change(ctx, r.ownerUID, acct.ID, app.ProviderChange{Models: &keepModels}); err != nil {
		t.Fatal(err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT misses, retired FROM gateway.provider_models WHERE account_id = $1::uuid AND model = 'm-route'`, acct.ID).
		Scan(&misses, &retired); err != nil {
		t.Fatal(err)
	}
	if misses != 1 || retired {
		t.Fatalf("Update reset misses/retired: misses=%d retired=%v, want misses=1 retired=false", misses, retired)
	}

	// 2nd consecutive miss: retires m-route, m-agent, m-unused; notifies active settings:manage users for in-use models only.
	r.runCheckJob(acct.ID)

	listed, err := r.svc.List(ctx, r.memUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed accounts = %d, want 1", len(listed))
	}
	if !slices.Equal(domain.UsableModels(listed[0]), []string{"m-keep"}) {
		t.Fatalf("UsableModels = %v, want [m-keep]", domain.UsableModels(listed[0]))
	}
	var retiredShown bool
	for _, em := range listed[0].EnabledModels {
		if em.ID == "m-route" && em.Retired {
			retiredShown = true
		}
	}
	if !retiredShown {
		t.Fatalf("EnabledModels missing retired:true on m-route: %+v", listed[0].EnabledModels)
	}
	if domain.UsableAgent(domain.AgentView{Enabled: true, ModelProvider: "openai", ModelID: "m-agent"}, nil, listed, "") {
		t.Fatal("UsableAgent accepted retired model")
	}
	if domain.UsableRoute(domain.UsableRouteView{Name: "r", Capability: domain.CapChat, Hops: []domain.UsableHop{{AccountID: acct.ID, Model: "m-route"}}}, listed) {
		t.Fatal("UsableRoute accepted retired hop")
	}
	if _, ok, err := r.store.PinAccount(ctx, r.memUID, acct.ID, "m-route"); err != nil || ok {
		t.Fatalf("PinAccount accepted retired model: ok=%v err=%v", ok, err)
	}
	if _, ok, err := r.store.PinProvider(ctx, r.memUID, nil, "openai", "m-agent"); err != nil || ok {
		t.Fatalf("PinProvider accepted retired model: ok=%v err=%v", ok, err)
	}
	route, err := r.store.ByName(ctx, "retire-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(route.Links) != 1 || route.Links[0].Model != "m-keep" {
		t.Fatalf("ByName route links = %+v, want only m-keep", route.Links)
	}
	if sugg, ok, err := r.store.SuggestModelForInput(ctx, acct.ID, "openai", domain.InputText); err != nil || !ok || sugg != "m-keep" {
		t.Fatalf("SuggestModelForInput = (%q, %v, %v), want (m-keep, true, nil)", sugg, ok, err)
	}

	// Update while retired must also keep misses=2, retired=true.
	if _, err := r.svc.Change(ctx, r.ownerUID, acct.ID, app.ProviderChange{Models: &keepModels}); err != nil {
		t.Fatal(err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT misses, retired FROM gateway.provider_models WHERE account_id = $1::uuid AND model = 'm-route'`, acct.ID).
		Scan(&misses, &retired); err != nil {
		t.Fatal(err)
	}
	if misses != 2 || !retired {
		t.Fatalf("Update cleared retired state: misses=%d retired=%v, want 2, true", misses, retired)
	}

	// Recipients: owner + active admin get 2 notices (m-route, m-agent), 0 for m-unused.
	for _, uid := range []string{r.ownerUID, r.adminUID} {
		ns := r.noticesFor(uid)
		if len(ns) != 2 {
			t.Fatalf("user %s notices = %d (%+v), want 2", uid, len(ns), ns)
		}
		joined := ns[0].Title + "\n" + ns[1].Title
		if !strings.Contains(joined, "m-route") || !strings.Contains(joined, "m-agent") || strings.Contains(joined, "m-unused") {
			t.Fatalf("unexpected notice titles for %s: %q", uid, joined)
		}
	}
	for _, uid := range []string{r.memUID, disabledAdminUID, unjoinedAdminUID} {
		if n := len(r.noticesFor(uid)); n != 0 {
			t.Fatalf("non-recipient %s got %d notices, want 0", uid, n)
		}
	}

	// Repeat check sends 0 duplicate notices.
	r.runCheckJob(acct.ID)
	if n := len(r.noticesFor(r.ownerUID)); n != 2 {
		t.Fatalf("notices after repeat miss = %d, want 2", n)
	}

	// Reappearance in discovery clears misses, retired flag, and retired:<model> cause.
	discovered = []domain.SeenModel{{ID: "m-route"}, {ID: "m-agent"}, {ID: "m-unused"}, {ID: "m-keep"}}
	r.runCheckJob(acct.ID)
	if err := r.owner.QueryRow(ctx, `SELECT misses, retired FROM gateway.provider_models WHERE account_id = $1::uuid AND model = 'm-route'`, acct.ID).
		Scan(&misses, &retired); err != nil {
		t.Fatal(err)
	}
	if misses != 0 || retired {
		t.Fatalf("after reappearance: misses=%d retired=%v, want 0, false", misses, retired)
	}
	if _, ok, err := r.store.PinAccount(ctx, r.memUID, acct.ID, "m-route"); err != nil || !ok {
		t.Fatalf("PinAccount after reappearance = (%v, %v), want true", ok, err)
	}
	var causes []string
	if err := r.owner.QueryRow(ctx, `SELECT notified_causes FROM gateway.provider_accounts WHERE id = $1::uuid`, acct.ID).
		Scan(&causes); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(causes, "retired:m-route") || slices.Contains(causes, "retired:m-agent") {
		t.Fatalf("notified_causes still contains cleared retired cause: %v", causes)
	}

	// Disappearing twice again retires again and sends a second notice.
	discovered = []domain.SeenModel{{ID: "m-keep"}}
	r.runCheckJob(acct.ID)
	if n := len(r.noticesFor(r.ownerUID)); n != 2 {
		t.Fatalf("notices after 1st post-recovery miss = %d, want 2", n)
	}
	r.runCheckJob(acct.ID)
	for _, uid := range []string{r.ownerUID, r.adminUID} {
		if n := len(r.noticesFor(uid)); n != 4 {
			t.Fatalf("user %s notices after 2nd retirement = %d, want 4", uid, n)
		}
	}
}

func TestFeature_UPM364_DeprecatedInUseNotifiesOncePerAccountAndCause(t *testing.T) {
	r := newCheckRig(t)
	ctx := context.Background()

	if err := r.store.ReplaceCatalog(ctx, "v1", []domain.CatalogRow{
		{Vendor: "openai", Model: "dep-used", Version: "v1", Kind: domain.KindChat, Lifecycle: domain.LifecycleDeprecated},
		{Vendor: "openai", Model: "dep-unused", Version: "v1", Kind: domain.KindChat, Lifecycle: domain.LifecycleDeprecated},
		{Vendor: "openai", Model: "active-used", Version: "v1", Kind: domain.KindChat, Lifecycle: domain.LifecycleActive},
	}); err != nil {
		t.Fatal(err)
	}

	privAcct := r.addAccount(r.memUID, app.NewProvider{
		Provider: "openai", Name: "Member OpenAI", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: adaKey,
		Models: []string{"dep-used", "dep-unused", "active-used"},
	})
	teamAcct := r.addAccount(r.ownerUID, app.NewProvider{
		Provider: "openai", Name: "Team OpenAI", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: newKey,
		Models:     []string{"dep-used", "dep-unused"},
		Visibility: domain.VisibilityTeam,
		TeamIDs:    []string{r.teamID},
	})

	// Route links dep-used and active-used on private account; agent uses dep-used on team account.
	var routeID string
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO gateway.routes (owner_user_id, visibility, name, capability)
		VALUES ($1::uuid, 'private', 'member-dep-route', 'chat')
		RETURNING id::text`, r.memUID).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
		VALUES ($1::uuid, 1, $2::uuid, 'dep-used'), ($1::uuid, 2, $2::uuid, 'active-used')`, routeID, privAcct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO agents.agents (owner_user_id, visibility, team_ids, name, slug, default_model_provider, default_model_id)
		VALUES ($1::uuid, 'team', ARRAY[$2::uuid], 'Team Agent', 'team-agent', 'openai', 'dep-used')`,
		r.memUID, r.teamID); err != nil {
		t.Fatal(err)
	}

	r.prober.handler = func(app.Attempt) probeReply {
		return probeReply{
			models: []domain.SeenModel{{ID: "dep-used"}, {ID: "dep-unused"}, {ID: "active-used"}},
			class:  domain.ClassOK,
		}
	}

	r.runCheckJob(privAcct.ID)
	r.runCheckJob(teamAcct.ID)

	// Private account notifies only member; team account notifies only active settings:manage users.
	memNotices := r.noticesFor(r.memUID)
	if len(memNotices) != 1 || !strings.Contains(memNotices[0].Title, "Member OpenAI") || !strings.Contains(memNotices[0].Title, "dep-used") {
		t.Fatalf("member notices = %+v, want 1 for Member OpenAI dep-used", memNotices)
	}
	for _, uid := range []string{r.ownerUID, r.adminUID} {
		ns := r.noticesFor(uid)
		if len(ns) != 1 || !strings.Contains(ns[0].Title, "Team OpenAI") || !strings.Contains(ns[0].Title, "dep-used") {
			t.Fatalf("manager %s notices = %+v, want 1 for Team OpenAI dep-used", uid, ns)
		}
	}

	// Repeat check sends 0 new notices.
	r.runCheckJob(privAcct.ID)
	r.runCheckJob(teamAcct.ID)
	if len(r.noticesFor(r.memUID)) != 1 || len(r.noticesFor(r.ownerUID)) != 1 {
		t.Fatal("repeat check sent duplicate deprecated notices")
	}

	// If catalog clears deprecated then re-deprecates, a new notice is sent.
	if err := r.store.ReplaceCatalog(ctx, "v2", []domain.CatalogRow{
		{Vendor: "openai", Model: "dep-used", Version: "v2", Kind: domain.KindChat, Lifecycle: domain.LifecycleActive},
	}); err != nil {
		t.Fatal(err)
	}
	r.runCheckJob(privAcct.ID)
	if err := r.store.ReplaceCatalog(ctx, "v3", []domain.CatalogRow{
		{Vendor: "openai", Model: "dep-used", Version: "v3", Kind: domain.KindChat, Lifecycle: domain.LifecycleDeprecated},
	}); err != nil {
		t.Fatal(err)
	}
	r.runCheckJob(privAcct.ID)
	if n := len(r.noticesFor(r.memUID)); n != 2 {
		t.Fatalf("member notices after re-deprecation = %d, want 2", n)
	}
}

func TestFeature_UPM364_TimeoutAnd5xxChangeStatusOnlyAndRetireNothing(t *testing.T) {
	r := newCheckRig(t)
	ctx := context.Background()

	acct := r.addAccount(r.memUID, app.NewProvider{
		Provider: "openai", Name: "Flaky OpenAI", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: adaKey,
		Models: []string{"gpt-4o", "gpt-4o-mini"},
	})

	// Seed prior discovery and 1 miss on gpt-4o.
	r.prober.handler = func(app.Attempt) probeReply {
		return probeReply{models: []domain.SeenModel{{ID: "gpt-4o-mini"}}, class: domain.ClassOK}
	}
	r.runCheckJob(acct.ID)

	for _, tc := range []struct {
		name  string
		class domain.FailureClass
	}{
		{name: "timeout", class: domain.ClassTimeout},
		{name: "5xx transient", class: domain.ClassTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.prober.handler = func(app.Attempt) probeReply {
				return probeReply{class: tc.class, err: errors.New("upstream timeout / 503")}
			}
			r.runCheckJob(acct.ID)

			got := r.getAccount(r.memUID, acct.ID)
			if got.Status != domain.StatusOffline {
				t.Fatalf("status = %q, want %q", got.Status, domain.StatusOffline)
			}

			seen, err := r.svc.SeenModels(ctx, r.memUID, acct.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(seen) != 1 || seen[0].ID != "gpt-4o-mini" {
				t.Fatalf("account_models_seen mutated on failure: %+v", seen)
			}

			var misses int
			var retired bool
			if err := r.owner.QueryRow(ctx, `SELECT misses, retired FROM gateway.provider_models WHERE account_id = $1::uuid AND model = 'gpt-4o'`, acct.ID).
				Scan(&misses, &retired); err != nil {
				t.Fatal(err)
			}
			if misses != 1 || retired {
				t.Fatalf("provider_models mutated on %s: misses=%d retired=%v, want 1, false", tc.name, misses, retired)
			}
			if n := len(r.noticesFor(r.memUID)); n != 0 {
				t.Fatalf("unexpected notices on %s: %d", tc.name, n)
			}
		})
	}
}

func TestFeature_UPM364_NoSecretInLogsOrNotices(t *testing.T) {
	r := newCheckRig(t)
	ctx := context.Background()

	const (
		secretKey = "sk-super-secret-must-never-leak-9999"
		hint      = "9999"
	)
	acct := r.addAccount(r.memUID, app.NewProvider{
		Provider: "openai", Name: "Secret Check", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: secretKey,
		Models: []string{"m-ret"},
	})
	var credRef string
	if err := r.owner.QueryRow(ctx, `SELECT credential_ref FROM gateway.provider_accounts WHERE id = $1::uuid`, acct.ID).Scan(&credRef); err != nil {
		t.Fatal(err)
	}
	var routeID string
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO gateway.routes (owner_user_id, visibility, name, capability)
		VALUES ($1::uuid, 'private', 'secret-route', 'chat')
		RETURNING id::text`, r.memUID).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
		VALUES ($1::uuid, 1, $2::uuid, 'm-ret')`, routeID, acct.ID); err != nil {
		t.Fatal(err)
	}

	rawLeakErr := fmt.Errorf("POST failed with key=%s ref=%s", secretKey, credRef)
	var reply probeReply
	r.prober.handler = func(app.Attempt) probeReply { return reply }

	// Rejected key run.
	reply = probeReply{class: domain.ClassCredential, err: rawLeakErr}
	r.runCheckJob(acct.ID)

	// Offline run.
	reply = probeReply{class: domain.ClassTimeout, err: rawLeakErr}
	r.runCheckJob(acct.ID)

	// Two misses to retire m-ret.
	reply = probeReply{class: domain.ClassOK, models: []domain.SeenModel{{ID: "m-other"}}}
	r.runCheckJob(acct.ID)
	r.runCheckJob(acct.ID)

	logText := r.logs.String()
	for _, forbidden := range []string{secretKey, hint, credRef, "POST failed with"} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("logs leaked %q:\n%s", forbidden, logText)
		}
	}

	var noticeTitles []string
	rows, err := r.owner.Query(ctx, `SELECT title FROM notices.notifications`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		noticeTitles = append(noticeTitles, title)
		for _, forbidden := range []string{secretKey, hint, credRef, "POST failed with"} {
			if strings.Contains(title, forbidden) {
				t.Fatalf("notice title leaked %q: %q", forbidden, title)
			}
		}
	}
	if len(noticeTitles) != 2 {
		t.Fatalf("notice titles = %v, want 2 (key rejected + retired model)", noticeTitles)
	}
}

func TestFeature_UPM364_RetrySendsNoticeOnceAfterInsertFailure(t *testing.T) {
	r := newCheckRig(t)
	ctx := context.Background()

	acct := r.addAccount(r.ownerUID, app.NewProvider{
		Provider: "openai", Name: "Retry OpenAI", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: adaKey,
		Models: []string{"gpt-4o"}, Visibility: domain.VisibilityEveryone,
	})

	r.prober.handler = func(app.Attempt) probeReply {
		return probeReply{class: domain.ClassCredential, err: errors.New("401 unauthorized")}
	}
	r.setNotifyErr(func(call int, _, _ string) error {
		if call == 1 {
			return errors.New("injected transient notice insert failure")
		}
		return nil
	})

	tx, err := r.appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := r.work.InsertTx(ctx, tx, ProviderCheckArgs{AccountID: acct.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r.waitJobEventuallyCompleted(res.Job.ID)

	var attempt int
	if err := r.owner.QueryRow(ctx, `SELECT attempt FROM platform.river_job WHERE id = $1`, res.Job.ID).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	if attempt < 2 {
		t.Fatalf("job attempt = %d, want >= 2 after first-insert failure", attempt)
	}

	for _, uid := range []string{r.ownerUID, r.adminUID} {
		ns := r.noticesFor(uid)
		if len(ns) != 1 || !strings.Contains(ns[0].Title, "Retry OpenAI") {
			t.Fatalf("recipient %s notices = %+v, want 1 for Retry OpenAI", uid, ns)
		}
	}
}

func TestFeature_UPM364_UpdateSerializesWithApplyAccountProbe(t *testing.T) {
	r := newCheckRig(t)
	ctx := context.Background()

	acct := r.addAccount(r.ownerUID, app.NewProvider{
		Provider: "openai", Name: "Race OpenAI", Adapter: domain.AdapterOpenAI,
		PlanKind: domain.PlanMetered, BaseURL: "https://api.example.com", APIKey: adaKey,
		Models: []string{"m-miss", "m-keep"}, Visibility: domain.VisibilityEveryone,
	})

	// Record 1 miss on m-miss so the next probe miss retires it.
	r.prober.handler = func(app.Attempt) probeReply {
		return probeReply{models: []domain.SeenModel{{ID: "m-keep"}}, class: domain.ClassOK}
	}
	r.runCheckJob(acct.ID)

	lockTx, err := r.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()

	var holderPID int32
	if err := lockTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	var one int
	if err := lockTx.QueryRow(ctx, `
		SELECT 1 FROM gateway.provider_accounts
		WHERE id = $1::uuid
		FOR UPDATE`, acct.ID).Scan(&one); err != nil {
		t.Fatal(err)
	}

	models := []string{"m-miss", "m-keep"}
	errCh := make(chan error, 1)
	var finished atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := r.svc.Change(ctx, r.ownerUID, acct.ID, app.ProviderChange{Models: &models})
		finished.Add(1)
		errCh <- err
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if n := finished.Load(); n > 0 {
			t.Fatalf("Providers.Update finished before account lock committed (%d)", n)
		}
		var blocked int
		err := r.owner.QueryRow(waitCtx, `
			SELECT count(*)
			FROM pg_stat_activity a
			JOIN pg_locks l ON l.pid = a.pid AND NOT l.granted
			WHERE a.datname = current_database()
			  AND a.pid <> pg_backend_pid()
			  AND a.wait_event_type = 'Lock'
			  AND $1 = ANY(pg_blocking_pids(a.pid))`,
			holderPID,
		).Scan(&blocked)
		if err != nil {
			t.Fatalf("wait for blocked Providers.Update: %v", err)
		}
		if blocked == 1 {
			break
		}
		runtime.Gosched()
	}

	if _, err := postgres.ApplyAccountProbeTx(ctx, lockTx, acct.ID, []domain.SeenModel{{ID: "m-keep"}}); err != nil {
		t.Fatal(err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	wg.Wait()
	if err := <-errCh; err != nil {
		t.Fatalf("Providers.Update failed: %v", err)
	}

	var misses int
	var retired, listed bool
	if err := r.owner.QueryRow(ctx, `
		SELECT misses, retired, listed
		FROM gateway.provider_models
		WHERE account_id = $1::uuid AND model = 'm-miss'`, acct.ID).
		Scan(&misses, &retired, &listed); err != nil {
		t.Fatal(err)
	}
	if misses != 2 || !retired || listed {
		t.Fatalf("after concurrent probe + Update: misses=%d retired=%v listed=%v, want misses=2 retired=true listed=false",
			misses, retired, listed)
	}
}
