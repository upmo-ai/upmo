//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway"
	httpapi "github.com/upmo-ai/upmo/internal/gateway/adapters/http"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

type limitAdapter struct {
	mu     sync.Mutex
	calls  int64
	delay  time.Duration
	usage  domain.Usage
	fail   domain.FailureClass
	onCall func()
}

func (a *limitAdapter) recordCall() {
	atomic.AddInt64(&a.calls, 1)
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	if a.onCall != nil {
		a.onCall()
	}
}

func (a *limitAdapter) callCount() int64 {
	return atomic.LoadInt64(&a.calls)
}

func (a *limitAdapter) currentUsage() domain.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage
}

func (a *limitAdapter) Decide(_ context.Context, _ app.Attempt, _ domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error) {
	a.recordCall()
	if a.fail != "" {
		return domain.DecideResult{}, domain.Usage{}, a.fail, fmt.Errorf("limit test: %s", a.fail)
	}
	return domain.DecideResult{YesNo: true}, a.currentUsage(), domain.ClassOK, nil
}

func (a *limitAdapter) Chat(_ context.Context, attempt app.Attempt, _ domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	a.recordCall()
	if a.fail != "" {
		return domain.ChatResult{}, a.fail, fmt.Errorf("limit test: %s", a.fail)
	}
	return domain.ChatResult{Text: "from " + attempt.Model, Usage: a.currentUsage()}, domain.ClassOK, nil
}

func (a *limitAdapter) ChatStream(_ context.Context, attempt app.Attempt, _ domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	a.recordCall()
	if a.fail != "" {
		return domain.Usage{}, a.fail, fmt.Errorf("limit test: %s", a.fail)
	}
	if err := emit("from " + attempt.Model); err != nil {
		return domain.Usage{}, domain.ClassCancelled, err
	}
	return a.currentUsage(), domain.ClassOK, nil
}

func (a *limitAdapter) Embed(_ context.Context, _ app.Attempt, inputs []string) ([][]float32, domain.Usage, domain.FailureClass, error) {
	a.recordCall()
	if a.fail != "" {
		return nil, domain.Usage{}, a.fail, fmt.Errorf("limit test: %s", a.fail)
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = make([]float32, domain.EmbedDim)
		vectors[i][0] = 1
	}
	return vectors, a.currentUsage(), domain.ClassOK, nil
}

func setUsageLimit(t *testing.T, w pinWorld, kind, subjectID string, amountMicroUSD int64) {
	t.Helper()
	var subj any
	if subjectID != "" {
		subj = subjectID
	}
	if _, err := w.pool.Exec(context.Background(), `
		INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd)
		VALUES ($1, $2::uuid, $3)
		ON CONFLICT (kind, subject_id) DO UPDATE SET amount_micro_usd = EXCLUDED.amount_micro_usd, updated_at = now()`,
		kind, subj, amountMicroUSD,
	); err != nil {
		t.Fatal(err)
	}
}

func seedDecideRoute(t *testing.T, w pinWorld, name string) {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO gateway.routes (name, capability, decision, installed)
		VALUES ($1, 'decide', 'yesno', true)
		RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
		VALUES ($1::uuid, 1, $2::uuid, 'm1', 0)`, id, pinHouse); err != nil {
		t.Fatal(err)
	}
}

func attachLimitAdapter(w *pinWorld, a *limitAdapter) {
	w.disp.Decide = map[string]app.Decider{domain.AdapterFake: a}
	w.disp.Chat = map[string]app.Chatter{domain.AdapterFake: a}
	w.disp.Stream = map[string]app.StreamChatter{domain.AdapterFake: a}
	w.disp.Embed = map[string]app.Embedder{domain.AdapterFake: a}
}

type barrierLimits struct {
	inner app.Limits
	ready sync.WaitGroup
	goCh  chan struct{}
}

func (b *barrierLimits) Reserve(ctx context.Context, req app.ReserveRequest) (app.Reservation, error) {
	b.ready.Done()
	<-b.goCh
	return b.inner.Reserve(ctx, req)
}

func (b *barrierLimits) Settle(ctx context.Context, res app.Reservation, row domain.Entry) error {
	return b.inner.Settle(ctx, res, row)
}

func (b *barrierLimits) Release(ctx context.Context, res app.Reservation) error {
	return b.inner.Release(ctx, res)
}

func TestFeature_UPM355_ConcurrentCallsOvershootAtMostInFlight(t *testing.T) {
	runBurst := func(t *testing.T, limitKind, subjectID string, pricePerMTok float64, usage domain.Usage, limitMicroUSD int64) (okCount, refusedCount int, spent, held int64) {
		t.Helper()
		w := newPinWorld(t, nil)
		ctx := context.Background()
		store := postgres.Store{Pool: w.pool}

		if err := store.ReplaceCatalog(ctx, "v1", []domain.CatalogRow{{
			Vendor: "fake", Model: "m1", Version: "v1",
			Price: domain.Price{InputPerMTok: &pricePerMTok, OutputPerMTok: &pricePerMTok},
		}}); err != nil {
			t.Fatal(err)
		}
		w.disp.Prices = store

		adapter := &limitAdapter{
			delay: 25 * time.Millisecond,
			usage: usage,
		}
		attachLimitAdapter(&w, adapter)
		fixedNow := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
		w.disp.Now = func() time.Time { return fixedNow }
		setUsageLimit(t, w, limitKind, subjectID, limitMicroUSD)

		const goroutines = 20
		cfg := w.pool.Config().Copy()
		cfg.MaxConns = goroutines
		burstPool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer burstPool.Close()
		warmConns := make([]*pgxpool.Conn, goroutines)
		for i := range goroutines {
			c, err := burstPool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			warmConns[i] = c
		}
		for _, c := range warmConns {
			c.Release()
		}

		burstStore := postgres.Store{Pool: burstPool}
		// Pre-create the spend rows via a released reservation so the burst tests
		// FOR UPDATE on existing rows rather than initial INSERT ON CONFLICT locking.
		warmup, err := burstStore.Reserve(ctx, app.ReserveRequest{
			UserID:      pinAuthor,
			Source:      domain.SourceChat,
			Estimate:    1,
			PeriodStart: domain.PeriodStart(fixedNow, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := burstStore.Release(ctx, warmup); err != nil {
			t.Fatal(err)
		}

		barrier := &barrierLimits{inner: burstStore, goCh: make(chan struct{})}
		barrier.ready.Add(goroutines)
		w.disp.Limits = barrier

		errs := make(chan error, goroutines)
		var wg sync.WaitGroup
		for range goroutines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, domain.Input{
					Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
				})
				errs <- err
			}()
		}
		barrier.ready.Wait()
		close(barrier.goCh)
		wg.Wait()
		close(errs)

		for err := range errs {
			if err == nil {
				okCount++
				continue
			}
			var reached domain.LimitReached
			if !errors.As(err, &reached) {
				t.Fatalf("unexpected error: %v", err)
			}
			refusedCount++
		}
		if subjectID == "" {
			if err := w.pool.QueryRow(ctx, `
				SELECT spent_micro_usd, held_micro_usd
				FROM gateway.usage_spend
				WHERE kind = $1 AND subject_id IS NULL`, limitKind).Scan(&spent, &held); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := w.pool.QueryRow(ctx, `
				SELECT spent_micro_usd, held_micro_usd
				FROM gateway.usage_spend
				WHERE kind = $1 AND subject_id = $2::uuid`, limitKind, subjectID).Scan(&spent, &held); err != nil {
				t.Fatal(err)
			}
		}
		if held != 0 {
			t.Fatalf("held_micro_usd = %d, want 0 after settlement", held)
		}
		return okCount, refusedCount, spent, held
	}

	t.Run("estimate equals cost", func(t *testing.T) {
		// Price m1 at $1,000 / MTok (1,000 micro-USD per token). Input "hi" (2 bytes -> 1 token)
		// gives a 1,000 micro-USD estimate, equal to the 1-token actual cost. With a 1,500
		// micro-USD workspace limit, FOR UPDATE locking on the workspace spend row must admit
		// exactly 1 call and keep spent <= limit.
		const limitMicroUSD = 1500
		const callCost = 1000
		okCount, refusedCount, spent, _ := runBurst(t, domain.LimitWorkspace, "", 1000.0, domain.Usage{Arrived: true, Input: 1, Output: 0}, limitMicroUSD)
		if okCount != 1 || refusedCount != 19 {
			t.Fatalf("ok=%d refused=%d, want ok=1 refused=19", okCount, refusedCount)
		}
		if spent != callCost || spent > limitMicroUSD {
			t.Fatalf("spent = %d, want %d (<= %d)", spent, callCost, limitMicroUSD)
		}
	})

	t.Run("real cost exceeds estimate", func(t *testing.T) {
		// Price m1 at $100 / MTok (100 micro-USD per token). Estimate is 100 micro-USD
		// while actual cost (10 tokens = 1,000 micro-USD) is larger. With limit 350, at most
		// 3 in-flight holds (3 * 100 <= 350) can pass concurrently, bounding overshoot.
		const limitMicroUSD = 350
		const maxInFlight = 3
		const callCost = 1000
		okCount, refusedCount, spent, _ := runBurst(t, domain.LimitPerson, pinAuthor, 100.0, domain.Usage{Arrived: true, Input: 1, Output: 9}, limitMicroUSD)
		if okCount < 1 || okCount > maxInFlight {
			t.Fatalf("ok=%d, want 1 <= ok <= %d", okCount, maxInFlight)
		}
		if refusedCount < 20-maxInFlight {
			t.Fatalf("refused=%d, want >= %d", refusedCount, 20-maxInFlight)
		}
		if spent != int64(okCount)*callCost {
			t.Fatalf("spent = %d, want %d * %d", spent, okCount, callCost)
		}
		if spent > limitMicroUSD+maxInFlight*callCost || spent > int64(maxInFlight)*callCost {
			t.Fatalf("spent = %d, exceeds in-flight bound (maxInFlight=%d, callCost=%d, limit=%d)", spent, maxInFlight, callCost, limitMicroUSD)
		}
	})
}

func TestFeature_UPM355_RefusedCallNeverReachesAdapter(t *testing.T) {
	w := newPinWorld(t, nil)
	seedDecideRoute(t, w, "pin-decide")
	adapter := &limitAdapter{}
	attachLimitAdapter(&w, adapter)

	const autoID = "018f3b2c-7a10-7c4a-8e11-dddddddddddd"
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10)
	setUsageLimit(t, w, domain.LimitAutomation, autoID, 10)

	ctx := context.Background()
	principal := domain.Principal{User: pinAuthor, Source: domain.SourceChat}
	assertRefused := func(name string, err error, wantKind string) {
		t.Helper()
		var reached domain.LimitReached
		if !errors.As(err, &reached) {
			t.Fatalf("%s: err = %v, want LimitReached", name, err)
		}
		if reached.Kind != wantKind || !strings.Contains(err.Error(), wantKind) {
			t.Fatalf("%s: reached = %+v (msg %q), want kind %s", name, reached, err.Error(), wantKind)
		}
	}

	_, err := w.disp.ChatCall(ctx, "pin-house", "", principal, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	assertRefused("ChatCall", err, domain.LimitPerson)

	sink := &pinSink{}
	err = w.disp.ChatStreamPinned(ctx, "pin-house", "", principal, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	}, domain.Pin{}, 0, sink)
	assertRefused("ChatStreamPinned", err, domain.LimitPerson)

	_, err = w.disp.DecideCall(ctx, "pin-decide", "", principal, domain.DecideRequest{
		Kind: domain.DecisionYesNo, Prompt: "ok?",
	})
	assertRefused("DecideCall", err, domain.LimitPerson)

	_, _, err = w.disp.EmbedCall(ctx, "pin-ledger-embed", "", principal, []string{"hello"})
	assertRefused("EmbedCall", err, domain.LimitPerson)

	// Raise person limit so automation limit triggers specifically.
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10_000)
	_, err = w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{
		User: pinAuthor, Source: domain.SourceAutomation, AutomationID: autoID,
	}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
	assertRefused("automation ChatCall", err, domain.LimitAutomation)

	// Also verify HTTP/RPC edge maps LimitReached to 429 refusal, not 500, and logs limit/spent/held server-side.
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10)
	var logBuf bytes.Buffer
	mux := http.NewServeMux()
	httpapi.Handler{
		Dispatch: w.disp,
		Log:      slog.New(slog.NewTextHandler(&logBuf, nil)),
	}.Mount(mux)
	for _, tc := range []struct{ path, body string }{
		{httpapi.PathChat, fmt.Sprintf(`{"route":"pin-house","prompt":"hi","user_id":%q,"source":"chat"}`, pinAuthor)},
		{httpapi.PathChatStream, fmt.Sprintf(`{"route":"pin-house","prompt":"hi","user_id":%q,"source":"chat"}`, pinAuthor)},
		{httpapi.PathDecide, fmt.Sprintf(`{"route":"pin-decide","kind":"yesno","prompt":"hi","user_id":%q,"source":"chat"}`, pinAuthor)},
		{httpapi.PathEmbed, fmt.Sprintf(`{"route":"pin-ledger-embed","inputs":["hi"],"user_id":%q,"source":"chat"}`, pinAuthor)},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("HTTP %s status = %d, want %d (body: %s)", tc.path, rec.Code, http.StatusTooManyRequests, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), domain.LimitPerson) {
			t.Fatalf("HTTP %s body %q does not name limit", tc.path, rec.Body.String())
		}
	}
	for _, want := range []string{"kind=person", "subject_id=" + pinAuthor, "limit_micro_usd=10", "spent_micro_usd=0", "held_micro_usd=0"} {
		if !strings.Contains(logBuf.String(), want) {
			t.Fatalf("person limit log missing %q:\n%s", want, logBuf.String())
		}
	}

	// Workspace limit 429 body must name workspace only, without exposing spend totals.
	logBuf.Reset()
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10_000)
	setUsageLimit(t, w, domain.LimitWorkspace, "", 42)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.PathChat, strings.NewReader(
		fmt.Sprintf(`{"route":"pin-house","prompt":"hi","user_id":%q,"source":"chat"}`, pinAuthor),
	)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("workspace HTTP status = %d, want %d (body: %s)", rec.Code, http.StatusTooManyRequests, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), domain.LimitWorkspace) || strings.Contains(rec.Body.String(), "42") {
		t.Fatalf("workspace 429 body %q must name kind without totals", rec.Body.String())
	}
	for _, want := range []string{"kind=workspace", "limit_micro_usd=42", "spent_micro_usd=0", "held_micro_usd=0"} {
		if !strings.Contains(logBuf.String(), want) {
			t.Fatalf("workspace limit log missing %q:\n%s", want, logBuf.String())
		}
	}

	if got := adapter.callCount(); got != 0 {
		t.Fatalf("adapter call count = %d, want 0", got)
	}
	var ledgerCount int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM gateway.ledger`).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 0 {
		t.Fatalf("ledger rows = %d, want 0 for refused calls", ledgerCount)
	}
}

func TestFeature_UPM355_MonthResetInWorkspaceTimeZone(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	adapter := &limitAdapter{}
	attachLimitAdapter(&w, adapter)

	if _, err := w.pool.Exec(ctx, `UPDATE identity.workspace SET time_zone = 'America/New_York' WHERE id = true`); err != nil {
		t.Fatal(err)
	}
	w.disp.TimeZone = func(ctx context.Context) (string, error) {
		return identity.WorkspaceTimeZone(ctx, w.pool)
	}

	// 2026-12-01 04:30 UTC is 2026-11-30 23:30 EST (still November in New York).
	now := time.Date(2026, 12, 1, 4, 30, 0, 0, time.UTC)
	w.disp.Now = func() time.Time { return now }

	// Room for 1 call (estimate 75 micro-USD, limit 100 micro-USD).
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 100)

	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	principal := domain.Principal{User: pinAuthor, Source: domain.SourceChat}

	if _, err := w.disp.ChatCall(ctx, "pin-house", "", principal, in); err != nil {
		t.Fatalf("first Nov call failed: %v", err)
	}
	if _, err := w.disp.ChatCall(ctx, "pin-house", "", principal, in); err == nil {
		t.Fatal("second Nov call succeeded, want refusal")
	}

	// Advance 1 hour: 2026-12-01 05:30 UTC is 2026-12-01 00:30 EST (Dec 1 in New York).
	now = now.Add(time.Hour)
	if _, err := w.disp.ChatCall(ctx, "pin-house", "", principal, in); err != nil {
		t.Fatalf("first Dec call failed after month rollover: %v", err)
	}

	rows, err := w.pool.Query(ctx, `
		SELECT period_start::text, spent_micro_usd
		FROM gateway.usage_spend
		WHERE kind = 'person' AND subject_id = $1::uuid
		ORDER BY period_start`, pinAuthor)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p string
		var s int64
		if err := rows.Scan(&p, &s); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%d", p, s))
	}
	if len(got) != 2 || got[0] != "2026-11-01=75" || got[1] != "2026-12-01=75" {
		t.Fatalf("periods = %v, want [2026-11-01=75 2026-12-01=75]", got)
	}
}

func TestFeature_UPM355_WorkspaceLimitCappedByCeiling(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	adapter := &limitAdapter{}
	attachLimitAdapter(&w, adapter)

	setUsageLimit(t, w, domain.LimitWorkspace, "", 1000)
	setUsageLimit(t, w, domain.LimitCeiling, "", 100)

	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	if _, err := w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{Source: domain.SourceSystem}, in); err != nil {
		t.Fatalf("first call under ceiling failed: %v", err)
	}
	_, err := w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{Source: domain.SourceSystem}, in)
	var reached domain.LimitReached
	if !errors.As(err, &reached) {
		t.Fatalf("second call err = %v, want LimitReached", err)
	}
	if reached.Kind != domain.LimitWorkspace || reached.Limit != 100 {
		t.Fatalf("reached = %+v, want workspace limit capped at ceiling 100", reached)
	}
	if adapter.callCount() != 1 {
		t.Fatalf("adapter calls = %d, want 1", adapter.callCount())
	}
}

type failingLimitStore struct{ err error }

func (f failingLimitStore) Reserve(context.Context, app.ReserveRequest) (app.Reservation, error) {
	return app.Reservation{}, f.err
}

func (f failingLimitStore) Settle(context.Context, app.Reservation, domain.Entry) error {
	return f.err
}

func (f failingLimitStore) Release(context.Context, app.Reservation) error {
	return f.err
}

func TestFeature_UPM355_FailingLimitStoreFailsClosed(t *testing.T) {
	w := newPinWorld(t, nil)
	seedDecideRoute(t, w, "pin-decide")
	adapter := &limitAdapter{}
	attachLimitAdapter(&w, adapter)

	dbErr := errors.New("db unavailable")
	w.disp.Limits = failingLimitStore{err: dbErr}

	ctx := context.Background()
	principal := domain.Principal{User: pinAuthor, Source: domain.SourceChat}
	if _, err := w.disp.ChatCall(ctx, "pin-house", "", principal, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	}); !errors.Is(err, dbErr) {
		t.Fatalf("ChatCall err = %v, want %v", err, dbErr)
	}
	if err := w.disp.ChatStreamPinned(ctx, "pin-house", "", principal, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	}, domain.Pin{}, 0, &pinSink{}); !errors.Is(err, dbErr) {
		t.Fatalf("ChatStreamPinned err = %v, want %v", err, dbErr)
	}
	if _, err := w.disp.DecideCall(ctx, "pin-decide", "", principal, domain.DecideRequest{
		Kind: domain.DecisionYesNo, Prompt: "ok?",
	}); !errors.Is(err, dbErr) {
		t.Fatalf("DecideCall err = %v, want %v", err, dbErr)
	}
	if _, _, err := w.disp.EmbedCall(ctx, "pin-ledger-embed", "", principal, []string{"hello"}); !errors.Is(err, dbErr) {
		t.Fatalf("EmbedCall err = %v, want %v", err, dbErr)
	}
	if adapter.callCount() != 0 {
		t.Fatalf("adapter calls = %d, want 0", adapter.callCount())
	}
}

func TestFeature_UPM355_NightlyCheckDetectsMismatch(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	if err := jobs.Up(ctx, w.pool); err != nil {
		t.Fatal(err)
	}
	adapter := &limitAdapter{}
	attachLimitAdapter(&w, adapter)

	// Run a successful house call, an automation call, a system call, a failed
	// known-zero call, and an own-key private call.
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hello"}}}
	if _, err := w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, in); err != nil {
		t.Fatal(err)
	}
	const autoID = "018f3b2c-7a10-7c4a-8e11-eeeeeeeeeeee"
	if _, err := w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceAutomation, AutomationID: autoID}, in); err != nil {
		t.Fatal(err)
	}
	if _, err := w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{Source: domain.SourceSystem}, in); err != nil {
		t.Fatal(err)
	}
	if _, err := w.disp.ChatCall(ctx, "pin-mixed", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, in); err != nil {
		t.Fatal(err)
	}
	adapter.fail = domain.ClassRateLimit
	_, _ = w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, in)

	reg := metrics.NewRegistry()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	mismatches, err := gateway.CheckSpendOnce(ctx, w.pool, nil, reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("clean run mismatches = %+v, want none", mismatches)
	}

	// Corrupt the person counter and verify the mismatch is reported, logged,
	// and counted in Prometheus metrics without auto-correcting the table.
	var beforeCorrupt int64
	if err := w.pool.QueryRow(ctx, `
		UPDATE gateway.usage_spend
		SET spent_micro_usd = spent_micro_usd + 123
		WHERE kind = 'person' AND subject_id = $1::uuid
		RETURNING spent_micro_usd`, pinAuthor).Scan(&beforeCorrupt); err != nil {
		t.Fatal(err)
	}

	mismatches, err = gateway.CheckSpendOnce(ctx, w.pool, nil, reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 || mismatches[0].Kind != domain.LimitPerson || mismatches[0].SpendMicroUSD != mismatches[0].LedgerMicroUSD+123 {
		t.Fatalf("corrupted mismatches = %+v", mismatches)
	}
	if !strings.Contains(logBuf.String(), "gateway spend counter mismatch") {
		t.Fatalf("expected mismatch log, got %q", logBuf.String())
	}
	var metricsOut bytes.Buffer
	if err := reg.WriteTo(ctx, &metricsOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(metricsOut.String(), `upmo_gateway_spend_mismatches_total{kind="person"} 1`) {
		t.Fatalf("metrics output missing mismatch counter:\n%s", metricsOut.String())
	}
	var afterCheck int64
	if err := w.pool.QueryRow(ctx, `
		SELECT spent_micro_usd FROM gateway.usage_spend
		WHERE kind = 'person' AND subject_id = $1::uuid`, pinAuthor).Scan(&afterCheck); err != nil {
		t.Fatal(err)
	}
	if afterCheck != beforeCorrupt {
		t.Fatalf("nightly check mutated counter from %d to %d", beforeCorrupt, afterCheck)
	}

	// An active hold on the current period is not reported, while a stuck hold
	// on an ended period is reported, logged, counted, and left untouched.
	if _, err := w.pool.Exec(ctx, `
		UPDATE gateway.usage_spend
		SET spent_micro_usd = spent_micro_usd - 123,
		    held_micro_usd = 200
		WHERE kind = 'person' AND subject_id = $1::uuid`, pinAuthor); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO gateway.usage_spend (kind, subject_id, period_start, spent_micro_usd, held_micro_usd)
		VALUES ('workspace', NULL, '2020-01-01', 0, 500)`); err != nil {
		t.Fatal(err)
	}
	logBuf.Reset()
	mismatches, err = gateway.CheckSpendOnce(ctx, w.pool, nil, reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 || mismatches[0].Kind != domain.LimitWorkspace || mismatches[0].HeldMicroUSD != 500 || mismatches[0].PeriodStart.Format("2006-01-02") != "2020-01-01" {
		t.Fatalf("stuck hold mismatches = %+v, want workspace 2020-01-01 held=500", mismatches)
	}
	if !strings.Contains(logBuf.String(), "gateway stuck hold on ended period") {
		t.Fatalf("expected stuck hold log, got %q", logBuf.String())
	}
	metricsOut.Reset()
	if err := reg.WriteTo(ctx, &metricsOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(metricsOut.String(), `upmo_gateway_spend_mismatches_total{kind="workspace"} 1`) {
		t.Fatalf("metrics output missing workspace stuck hold counter:\n%s", metricsOut.String())
	}
	var stuckHeld int64
	if err := w.pool.QueryRow(ctx, `
		SELECT held_micro_usd FROM gateway.usage_spend
		WHERE kind = 'workspace' AND subject_id IS NULL AND period_start = '2020-01-01'`).Scan(&stuckHeld); err != nil {
		t.Fatal(err)
	}
	if stuckHeld != 500 {
		t.Fatalf("nightly check mutated stuck hold from 500 to %d", stuckHeld)
	}

	// Also verify River periodic job + worker registration builds and starts cleanly.
	workers := river.NewWorkers()
	gateway.AddSpendChecker(workers, w.pool, nil, reg, logger)
	workClient, err := jobs.Open(w.pool,
		jobs.WithWorkers(workers),
		jobs.WithPeriodic(gateway.SpendCheckPeriodic()),
		jobs.WithMetrics(reg),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := workClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := workClient.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFeature_UPM355_NonChargedCallsSkipLimitsAndCounters(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	adapter := &limitAdapter{}
	attachLimitAdapter(&w, adapter)

	const (
		pinSubAcc   = "018f3b2c-7a10-7c4a-8e11-000000000021"
		pinLocalAcc = "018f3b2c-7a10-7c4a-8e11-000000000022"
	)
	for _, acc := range []struct {
		id, name, ref, plan, model string
	}{
		{pinSubAcc, "limit-sub", "limit_sub", domain.PlanSubscription, "m-sub"},
		{pinLocalAcc, "limit-local", "limit_local", domain.PlanLocal, "m-local"},
	} {
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO gateway.provider_accounts
				(id, owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, visibility)
			VALUES ($1::uuid, NULL, 'fake', $2, 'fake', $3, $4, true, '', 'everyone')`,
			acc.id, acc.name, acc.ref, acc.plan); err != nil {
			t.Fatal(err)
		}
		var routeID string
		if err := w.pool.QueryRow(ctx, `
			INSERT INTO gateway.routes (name, capability, installed)
			VALUES ($1, 'chat', true) RETURNING id::text`, acc.name).Scan(&routeID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
			VALUES ($1::uuid, 1, $2::uuid, $3, 0)`, routeID, acc.id, acc.model); err != nil {
			t.Fatal(err)
		}
	}
	secrets := pinSecrets{"limit_sub": "k-sub", "limit_local": "k-local"}
	for k, v := range pinKeys {
		secrets[k] = v
	}
	w.disp.Secrets = secrets

	// Exhaust all limits to 0: any charged call would be refused.
	setUsageLimit(t, w, domain.LimitWorkspace, "", 0)
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 0)

	principal := domain.Principal{User: pinAuthor, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	// 1. Own-key private account (pinMine on pin-mixed).
	if _, err := w.disp.ChatCall(ctx, "pin-mixed", "", principal, in); err != nil {
		t.Fatalf("own-key call refused: %v", err)
	}
	// 2. Subscription house account.
	if _, err := w.disp.ChatCall(ctx, "limit-sub", "", principal, in); err != nil {
		t.Fatalf("subscription call refused: %v", err)
	}
	// 3. Local house account.
	if _, err := w.disp.ChatCall(ctx, "limit-local", "", principal, in); err != nil {
		t.Fatalf("local call refused: %v", err)
	}

	if got := adapter.callCount(); got != 3 {
		t.Fatalf("adapter calls = %d, want 3", got)
	}
	var spendRows int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_spend`).Scan(&spendRows); err != nil {
		t.Fatal(err)
	}
	if spendRows != 0 {
		t.Fatalf("usage_spend rows = %d, want 0", spendRows)
	}
	var chargedCount int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM gateway.ledger WHERE charged_to_limit`).Scan(&chargedCount); err != nil {
		t.Fatal(err)
	}
	if chargedCount != 0 {
		t.Fatalf("charged_to_limit ledger rows = %d, want 0", chargedCount)
	}
}

func TestFeature_UPM355_CancelledCallSettlesAndClearsHold(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	store := postgres.Store{Pool: w.pool}

	rate := 100.0
	if err := store.ReplaceCatalog(ctx, "v1", []domain.CatalogRow{{
		Vendor: "fake", Model: "m1", Version: "v1",
		Price: domain.Price{InputPerMTok: &rate, OutputPerMTok: &rate},
	}}); err != nil {
		t.Fatal(err)
	}
	w.disp.Prices = store

	callCtx, cancel := context.WithCancel(ctx)
	adapter := &limitAdapter{
		usage:  domain.Usage{Arrived: true, Input: 2, Output: 3},
		onCall: cancel,
	}
	attachLimitAdapter(&w, adapter)

	principal := domain.Principal{User: pinAuthor, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	if err := w.disp.ChatStreamPinned(callCtx, "pin-house", "", principal, in, domain.Pin{}, 0, &pinSink{}); err != nil {
		t.Fatalf("ChatStreamPinned failed: %v", err)
	}

	const wantCost = 500
	for _, kind := range []string{domain.LimitWorkspace, domain.LimitPerson} {
		var spent, held int64
		if err := w.pool.QueryRow(ctx, `
			SELECT spent_micro_usd, held_micro_usd
			FROM gateway.usage_spend
			WHERE kind = $1`, kind).Scan(&spent, &held); err != nil {
			t.Fatalf("%s query: %v", kind, err)
		}
		if held != 0 || spent != wantCost {
			t.Fatalf("%s spent=%d held=%d, want spent=%d held=0", kind, spent, held, wantCost)
		}
	}
	var ledgerCount int
	var costMicro int64
	var charged bool
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(SUM(cost_micro_usd), 0), bool_and(charged_to_limit)
		FROM gateway.ledger`).Scan(&ledgerCount, &costMicro, &charged); err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 1 || costMicro != wantCost || !charged {
		t.Fatalf("ledger count=%d cost=%d charged=%t, want 1, %d, true", ledgerCount, costMicro, charged, wantCost)
	}
}

type forcedSettleFailLimits struct {
	inner app.Limits
	err   error
}

func (f forcedSettleFailLimits) Reserve(ctx context.Context, req app.ReserveRequest) (app.Reservation, error) {
	return f.inner.Reserve(ctx, req)
}

func (f forcedSettleFailLimits) Settle(context.Context, app.Reservation, domain.Entry) error {
	return f.err
}

func (f forcedSettleFailLimits) Release(ctx context.Context, res app.Reservation) error {
	return f.inner.Release(ctx, res)
}

type failingReleaseLimits struct {
	inner app.Limits
	err   error
}

func (f failingReleaseLimits) Reserve(ctx context.Context, req app.ReserveRequest) (app.Reservation, error) {
	return f.inner.Reserve(ctx, req)
}

func (f failingReleaseLimits) Settle(ctx context.Context, res app.Reservation, row domain.Entry) error {
	return f.inner.Settle(ctx, res, row)
}

func (f failingReleaseLimits) Release(context.Context, app.Reservation) error {
	return f.err
}

type failingPrices struct{ err error }

func (f failingPrices) Price(context.Context, string, string) (domain.Price, bool, error) {
	return domain.Price{}, false, f.err
}

func TestFeature_UPM355_FailuresReleaseHoldSafely(t *testing.T) {
	principal := domain.Principal{User: pinAuthor, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	assertZeroBalances := func(t *testing.T, w pinWorld) {
		t.Helper()
		rows, err := w.pool.Query(context.Background(), `SELECT kind, spent_micro_usd, held_micro_usd FROM gateway.usage_spend`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var kind string
			var spent, held int64
			if err := rows.Scan(&kind, &spent, &held); err != nil {
				t.Fatal(err)
			}
			if spent != 0 || held != 0 {
				t.Fatalf("%s spent=%d held=%d, want 0/0", kind, spent, held)
			}
		}
	}

	t.Run("forced settle failure", func(t *testing.T) {
		w := newPinWorld(t, nil)
		attachLimitAdapter(&w, &limitAdapter{})
		settleErr := errors.New("forced settle failure")
		w.disp.Limits = forcedSettleFailLimits{inner: w.disp.Limits, err: settleErr}

		_, err := w.disp.ChatCall(context.Background(), "pin-house", "", principal, in)
		if !errors.Is(err, settleErr) {
			t.Fatalf("ChatCall err = %v, want %v", err, settleErr)
		}
		assertZeroBalances(t, w)
	})

	t.Run("pricing failure", func(t *testing.T) {
		w := newPinWorld(t, nil)
		attachLimitAdapter(&w, &limitAdapter{})
		priceErr := errors.New("pricing unavailable")
		w.disp.Prices = failingPrices{err: priceErr}

		_, err := w.disp.ChatCall(context.Background(), "pin-house", "", principal, in)
		if !errors.Is(err, priceErr) {
			t.Fatalf("ChatCall err = %v, want %v", err, priceErr)
		}
		assertZeroBalances(t, w)
	})

	t.Run("adapter panic", func(t *testing.T) {
		w := newPinWorld(t, nil)
		attachLimitAdapter(&w, &limitAdapter{onCall: func() { panic("boom") }})

		var panicked any
		func() {
			defer func() { panicked = recover() }()
			_, _ = w.disp.ChatCall(context.Background(), "pin-house", "", principal, in)
		}()
		if panicked != "boom" {
			t.Fatalf("panic = %v, want boom", panicked)
		}
		assertZeroBalances(t, w)
	})

	t.Run("release failure logs and counts metric", func(t *testing.T) {
		w := newPinWorld(t, nil)
		attachLimitAdapter(&w, &limitAdapter{})
		priceErr := errors.New("pricing unavailable")
		releaseErr := errors.New("forced release failure")
		reg := metrics.NewRegistry()
		var logBuf bytes.Buffer
		w.disp.Prices = failingPrices{err: priceErr}
		w.disp.Limits = failingReleaseLimits{inner: w.disp.Limits, err: releaseErr}
		w.disp.Metrics = reg
		w.disp.Log = slog.New(slog.NewTextHandler(&logBuf, nil))

		_, err := w.disp.ChatCall(context.Background(), "pin-house", "", principal, in)
		if !errors.Is(err, priceErr) {
			t.Fatalf("ChatCall err = %v, want %v", err, priceErr)
		}
		if !strings.Contains(logBuf.String(), "gateway release hold failed") || !strings.Contains(logBuf.String(), releaseErr.Error()) {
			t.Fatalf("release failure log = %q, want error recorded", logBuf.String())
		}
		rec := httptest.NewRecorder()
		reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if !strings.Contains(rec.Body.String(), "upmo_gateway_hold_release_failures_total 1") {
			t.Fatalf("metrics missing upmo_gateway_hold_release_failures_total 1:\n%s", rec.Body.String())
		}
	})

	t.Run("double release is idempotent", func(t *testing.T) {
		w := newPinWorld(t, nil)
		store := postgres.Store{Pool: w.pool}
		ctx := context.Background()
		period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		res, err := store.Reserve(ctx, app.ReserveRequest{
			UserID:      pinAuthor,
			Source:      domain.SourceChat,
			Estimate:    75,
			PeriodStart: period,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Release(ctx, res); err != nil {
			t.Fatalf("first Release: %v", err)
		}
		if err := store.Release(ctx, res); err != nil {
			t.Fatalf("second Release: %v", err)
		}
		// Settling an already-claimed hold must return an error instead of silently dropping the charge.
		if err := store.Settle(ctx, res, domain.Entry{
			Route: "pin-house", AccountID: pinHouse, Model: "m1",
			PlanKind: domain.PlanMetered, Class: domain.ClassOK, Principal: principal,
		}); err == nil {
			t.Fatal("Settle after Release succeeded, want already-claimed error")
		}
		assertZeroBalances(t, w)
		var ledgerCount int
		if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM gateway.ledger`).Scan(&ledgerCount); err != nil {
			t.Fatal(err)
		}
		if ledgerCount != 0 {
			t.Fatalf("ledger rows = %d, want 0", ledgerCount)
		}
	})
}

func TestFeature_UPM355_TimeZoneErrorRefusesCall(t *testing.T) {
	w := newPinWorld(t, nil)
	adapter := &limitAdapter{}
	attachLimitAdapter(&w, adapter)

	// Separate pgtest database with identity.workspace dropped so WorkspaceTimeZone hits a real DB error.
	badPool := pgtest.New(t)
	if _, err := badPool.Exec(context.Background(), `DROP TABLE identity.workspace CASCADE`); err != nil {
		t.Fatal(err)
	}
	w.disp.TimeZone = func(ctx context.Context) (string, error) {
		return identity.WorkspaceTimeZone(ctx, badPool)
	}

	_, err := w.disp.ChatCall(context.Background(), "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42P01" || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("ChatCall err = %v, want Postgres 42P01 (does not exist) error", err)
	}
	if adapter.callCount() != 0 {
		t.Fatalf("adapter calls = %d, want 0", adapter.callCount())
	}
}

func TestFeature_UPM355_SystemCallsCountOnlyAgainstWorkspace(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	adapter := &limitAdapter{}
	attachLimitAdapter(&w, adapter)

	// Set a person limit below one call's cost (75 micro-USD) and a workspace limit with room.
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10)
	setUsageLimit(t, w, domain.LimitWorkspace, "", 1000)

	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	// System call carrying a user_id must not check or charge the person row.
	if _, err := w.disp.ChatCall(ctx, "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceSystem}, in); err != nil {
		t.Fatalf("system call with user_id failed: %v", err)
	}

	var wsSpent, wsHeld int64
	if err := w.pool.QueryRow(ctx, `
		SELECT spent_micro_usd, held_micro_usd
		FROM gateway.usage_spend
		WHERE kind = 'workspace' AND subject_id IS NULL`).Scan(&wsSpent, &wsHeld); err != nil {
		t.Fatal(err)
	}
	if wsSpent != 75 || wsHeld != 0 {
		t.Fatalf("workspace spent=%d held=%d, want 75/0", wsSpent, wsHeld)
	}
	var personRows int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM gateway.usage_spend WHERE kind = 'person'`).Scan(&personRows); err != nil {
		t.Fatal(err)
	}
	if personRows != 0 {
		t.Fatalf("person spend rows = %d, want 0 for system call", personRows)
	}

	mismatches, err := gateway.CheckSpendOnce(ctx, w.pool, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("nightly check mismatches = %+v, want 0", mismatches)
	}
}

func TestFeature_UPM355_ReserveLocksSpendRows(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()

	cfg := w.pool.Config().Copy()
	cfg.MaxConns = 6
	lockPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer lockPool.Close()

	store := postgres.Store{Pool: lockPool}
	const estimate = int64(100)
	setUsageLimit(t, w, domain.LimitWorkspace, "", estimate)
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	// Ensure the workspace spend row exists before locking it.
	warmup, err := store.Reserve(ctx, app.ReserveRequest{
		Source:      domain.SourceSystem,
		Estimate:    1,
		PeriodStart: period,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, warmup); err != nil {
		t.Fatal(err)
	}

	lockTx, err := lockPool.Begin(ctx)
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
		SELECT 1
		FROM gateway.usage_spend
		WHERE kind = 'workspace'
		  AND subject_id IS NULL
		  AND period_start = $1::date
		FOR UPDATE`, period.Format("2006-01-02"),
	).Scan(&one); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	var finished atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Reserve(ctx, app.ReserveRequest{
				Source:      domain.SourceSystem,
				Estimate:    estimate,
				PeriodStart: period,
			})
			finished.Add(1)
			errs <- err
		}()
	}

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if n := finished.Load(); n > 0 {
			t.Fatalf("%d Reserve call(s) finished before lock transaction committed", n)
		}
		var blocked int
		err := lockPool.QueryRow(waitCtx, `
			WITH RECURSIVE blocked(pid) AS (
				SELECT a.pid
				FROM pg_stat_activity a
				JOIN pg_locks l ON l.pid = a.pid AND NOT l.granted
				WHERE a.datname = current_database()
				  AND a.pid <> pg_backend_pid()
				  AND a.wait_event_type = 'Lock'
				  AND $1 = ANY(pg_blocking_pids(a.pid))
				UNION
				SELECT a.pid
				FROM pg_stat_activity a
				JOIN pg_locks l ON l.pid = a.pid AND NOT l.granted
				JOIN blocked b ON b.pid = ANY(pg_blocking_pids(a.pid))
				WHERE a.datname = current_database()
				  AND a.pid <> pg_backend_pid()
				  AND a.wait_event_type = 'Lock'
			)
			SELECT count(*) FROM blocked`,
			holderPID,
		).Scan(&blocked)
		if err != nil {
			t.Fatalf("wait for blocked Reserve calls: %v", err)
		}
		if blocked == 2 {
			break
		}
		runtime.Gosched()
	}

	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)

	var okCount, refusedCount int
	for err := range errs {
		if err == nil {
			okCount++
			continue
		}
		var reached domain.LimitReached
		if !errors.As(err, &reached) {
			t.Fatalf("unexpected Reserve error: %v", err)
		}
		refusedCount++
	}
	if okCount != 1 || refusedCount != 1 {
		t.Fatalf("ok=%d refused=%d, want ok=1 refused=1", okCount, refusedCount)
	}

	var spent, held int64
	if err := w.pool.QueryRow(ctx, `
		SELECT spent_micro_usd, held_micro_usd
		FROM gateway.usage_spend
		WHERE kind = 'workspace'
		  AND subject_id IS NULL
		  AND period_start = $1::date`, period.Format("2006-01-02"),
	).Scan(&spent, &held); err != nil {
		t.Fatal(err)
	}
	if spent != 0 || held != estimate {
		t.Fatalf("spent=%d held=%d, want spent=0 held=%d", spent, held, estimate)
	}
}
