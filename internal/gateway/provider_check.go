package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
)

// ProviderCheckDispatchArgs is the daily River periodic payload that enqueues
// one spread-out check job per enabled provider account.
type ProviderCheckDispatchArgs struct{}

// Kind is the River job kind for dispatching provider checks.
func (ProviderCheckDispatchArgs) Kind() string { return "gateway_provider_check_dispatch" }

// ProviderCheckArgs is the River payload that checks one provider account for a
// given UTC day (YYYY-MM-DD).
type ProviderCheckArgs struct {
	AccountID string `json:"account_id"`
	Day       string `json:"day"`
}

// Kind is the River job kind for checking one provider account.
func (ProviderCheckArgs) Kind() string { return "gateway_provider_check" }

// ProviderCheckDeps provides the owner pool, secret chain, probes, and
// cross-context ports used by the daily provider check workers.
type ProviderCheckDeps struct {
	Owner               *pgxpool.Pool
	App                 *pgxpool.Pool
	Enqueuer            jobs.Enqueuer
	Secrets             app.Secrets
	Probes              map[string]app.Prober
	AgentModelUses      func(ctx context.Context, provider string, models []string) ([]string, error)
	AutomationModelUses func(ctx context.Context, provider string, models []string) ([]string, error)
	ActiveUser          func(ctx context.Context, userID string) (bool, error)
	SettingsManagers    func(ctx context.Context) ([]string, error)
	Notify              func(ctx context.Context, tx pgx.Tx, userID, title string) error
	Now                 func() time.Time
	Log                 *slog.Logger
}

type providerCheckDispatchWorker struct {
	river.WorkerDefaults[ProviderCheckDispatchArgs]
	deps ProviderCheckDeps
}

type providerCheckWorker struct {
	river.WorkerDefaults[ProviderCheckArgs]
	deps ProviderCheckDeps
}

// AddProviderChecker registers the daily dispatch worker and per-account check
// worker on workers.
func AddProviderChecker(workers *river.Workers, deps ProviderCheckDeps) {
	if deps.Probes == nil {
		deps.Probes = defaultProbes()
	}
	river.AddWorker(workers, &providerCheckDispatchWorker{deps: deps})
	river.AddWorker(workers, &providerCheckWorker{deps: deps})
}

// ProviderCheckPeriodic runs the provider check dispatch at startup and then
// once every 24 hours.
func ProviderCheckPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return ProviderCheckDispatchArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

func (w *providerCheckDispatchWorker) Work(ctx context.Context, _ *river.Job[ProviderCheckDispatchArgs]) error {
	deps := w.deps
	if deps.Enqueuer == nil {
		if client, err := river.ClientFromContextSafely[pgx.Tx](ctx); err == nil {
			deps.Enqueuer = client
		}
	}
	_, err := DispatchProviderChecksOnce(ctx, deps)
	return err
}

func (w *providerCheckWorker) Work(ctx context.Context, job *river.Job[ProviderCheckArgs]) error {
	return CheckAccountOnce(ctx, w.deps, job.Args.AccountID)
}

// DispatchProviderChecksOnce lists all enabled provider accounts on Owner and
// enqueues one ProviderCheckArgs per account for its next daily UTC slot
// (midnight UTC + AccountCheckOffset(id), or the next day if that slot has
// already passed), keyed by account and UTC day with 48-hour argument uniqueness.
func DispatchProviderChecksOnce(ctx context.Context, deps ProviderCheckDeps) (int, error) {
	ids, err := postgres.ListEnabledAccountIDs(ctx, deps.Owner)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if deps.Enqueuer == nil {
		return 0, fmt.Errorf("gateway: provider check enqueuer is nil")
	}
	txPool := deps.App
	if txPool == nil {
		txPool = deps.Owner
	}
	tx, err := txPool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now()
	if deps.Now != nil {
		now = deps.Now()
	}
	nowUTC := now.UTC()
	midnight := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)

	enqueued := 0
	for _, id := range ids {
		slot := midnight.Add(domain.AccountCheckOffset(id))
		if slot.Before(nowUTC) {
			slot = slot.AddDate(0, 0, 1)
		}
		day := slot.Format("2006-01-02")
		opts := &river.InsertOpts{
			ScheduledAt: slot,
			UniqueOpts: river.UniqueOpts{
				ByArgs:   true,
				ByPeriod: 48 * time.Hour,
			},
		}
		res, err := deps.Enqueuer.InsertTx(ctx, tx, ProviderCheckArgs{AccountID: id, Day: day}, opts)
		if err != nil {
			return 0, err
		}
		if res != nil && !res.UniqueSkippedAsDuplicate {
			enqueued++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return enqueued, nil
}

// CheckAccountOnce probes one enabled provider account, updates status/models/
// retirement state on Owner, and dispatches deduplicated failure notices.
func CheckAccountOnce(ctx context.Context, deps ProviderCheckDeps, accountID string) error {
	acct, found, err := postgres.GetAccountForCheck(ctx, deps.Owner, accountID)
	if err != nil {
		return err
	}
	if !found || !acct.Enabled {
		return nil
	}
	secret, ok := resolveCheckSecret(ctx, deps, acct)
	if !ok {
		return nil
	}
	probes := deps.Probes
	if probes == nil {
		probes = defaultProbes()
	}
	prober := probes[acct.Adapter]
	if prober == nil {
		return nil
	}

	callCtx, cancel := context.WithTimeout(ctx, app.TestTimeout)
	defer cancel()
	models, class, _ := prober.Probe(callCtx, app.Attempt{
		Adapter:  acct.Adapter,
		BaseURL:  acct.BaseURL,
		Secret:   secret,
		PlanKind: acct.PlanKind,
	})

	if class != domain.ClassOK {
		status := app.ProbeStatus(class)
		if err := postgres.UpdateAccountCheckStatus(ctx, deps.Owner, acct.ID, status); err != nil {
			return err
		}
		if class == domain.ClassCredential {
			title := fmt.Sprintf("Provider %s: API key was rejected", acct.Name)
			if err := notifyAccountCause(ctx, deps, acct, "key_rejected", title); err != nil {
				return err
			}
		}
		if deps.Log != nil {
			deps.Log.InfoContext(ctx, "provider check failed", "account", acct.ID, "status", status)
		}
		return nil
	}

	applied, err := postgres.ApplyAccountProbe(ctx, deps.Owner, acct.ID, models)
	if err != nil {
		return err
	}
	if deps.Log != nil {
		deps.Log.InfoContext(ctx, "provider check succeeded", "account", acct.ID, "status", domain.StatusConnected)
	}

	candidateSet := make(map[string]bool, len(applied.ProviderModels)+len(applied.RouteModelsInUse))
	inUseSet := make(map[string]bool, len(applied.RouteModelsInUse))
	for _, m := range applied.ProviderModels {
		candidateSet[m] = true
	}
	for _, m := range applied.RouteModelsInUse {
		candidateSet[m] = true
		inUseSet[m] = true
	}
	candidates := slices.Sorted(maps.Keys(candidateSet))
	if len(candidates) > 0 {
		if deps.AgentModelUses != nil {
			used, err := deps.AgentModelUses(ctx, acct.Provider, candidates)
			if err != nil {
				return err
			}
			for _, m := range used {
				inUseSet[m] = true
			}
		}
		if deps.AutomationModelUses != nil {
			used, err := deps.AutomationModelUses(ctx, acct.Provider, candidates)
			if err != nil {
				return err
			}
			for _, m := range used {
				inUseSet[m] = true
			}
		}
	}

	priceModelSet := make(map[string]bool, len(candidates))
	for _, m := range candidates {
		priceModelSet[m] = true
	}
	for _, c := range acct.NotifiedCauses {
		if m, ok := strings.CutPrefix(c, "deprecated:"); ok {
			priceModelSet[m] = true
		}
	}
	deprecatedMap, err := postgres.DeprecatedModels(ctx, deps.Owner, acct.Provider, slices.Sorted(maps.Keys(priceModelSet)))
	if err != nil {
		return err
	}
	var clearDeprecated []string
	for _, c := range acct.NotifiedCauses {
		if m, ok := strings.CutPrefix(c, "deprecated:"); ok && !deprecatedMap[m] {
			clearDeprecated = append(clearDeprecated, c)
		}
	}
	if len(clearDeprecated) > 0 {
		if err := postgres.ClearCauses(ctx, deps.Owner, acct.ID, clearDeprecated); err != nil {
			return err
		}
	}
	for _, m := range candidates {
		if deprecatedMap[m] && inUseSet[m] {
			title := fmt.Sprintf("Provider %s: model %s is deprecated", acct.Name, m)
			if err := notifyAccountCause(ctx, deps, acct, "deprecated:"+m, title); err != nil {
				return err
			}
		}
	}

	slices.Sort(applied.RetiredModels)
	for _, m := range applied.RetiredModels {
		if inUseSet[m] {
			title := fmt.Sprintf("Provider %s: model %s was retired by the provider", acct.Name, m)
			if err := notifyAccountCause(ctx, deps, acct, "retired:"+m, title); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveCheckSecret(ctx context.Context, deps ProviderCheckDeps, acct postgres.CheckAccount) (string, bool) {
	if deps.Secrets != nil {
		secret, err := deps.Secrets.Get(acct.CredentialRef)
		if err == nil {
			return secret, true
		}
	}
	if acct.PlanKind == domain.PlanLocal && !acct.HasKey {
		return "", true
	}
	if deps.Log != nil {
		deps.Log.WarnContext(ctx, "provider check skipped: credential unavailable", "account", acct.ID)
	}
	return "", false
}

func notifyAccountCause(ctx context.Context, deps ProviderCheckDeps, acct postgres.CheckAccount, cause, title string) error {
	if deps.Notify == nil {
		return nil
	}
	recipients, err := checkRecipients(ctx, deps, acct)
	if err != nil || len(recipients) == 0 {
		return err
	}
	if deps.Owner == nil {
		return fmt.Errorf("gateway: no database")
	}
	tx, err := deps.Owner.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	added, err := postgres.MarkCauseNotifiedTx(ctx, tx, acct.ID, cause)
	if err != nil || !added {
		return err
	}
	for _, uid := range recipients {
		if err := deps.Notify(ctx, tx, uid, title); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func checkRecipients(ctx context.Context, deps ProviderCheckDeps, acct postgres.CheckAccount) ([]string, error) {
	if acct.Visibility == domain.VisibilityPrivate && !acct.House {
		if acct.OwnerUserID == "" {
			return nil, nil
		}
		if deps.ActiveUser != nil {
			active, err := deps.ActiveUser(ctx, acct.OwnerUserID)
			if err != nil || !active {
				return nil, err
			}
		}
		return []string{acct.OwnerUserID}, nil
	}
	if deps.SettingsManagers == nil {
		return nil, nil
	}
	return deps.SettingsManagers(ctx)
}
