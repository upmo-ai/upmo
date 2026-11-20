package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/gateway/snapshot"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

const (
	// DefaultCatalogURL is the remote models.dev endpoint fetched daily.
	DefaultCatalogURL = "https://models.dev/api.json"
	// CatalogFetchTimeout bounds the HTTP fetch for a catalog refresh.
	CatalogFetchTimeout = 30 * time.Second
	// CatalogMaxBytes caps the uncompressed models.dev payload at 20 MiB.
	CatalogMaxBytes int64 = 20 << 20
	// CatalogStaleAfter is the age of catalog_meta.as_of after which a failed
	// refresh emits a single notice per stale episode.
	CatalogStaleAfter = 7 * 24 * time.Hour
	maxCatalogErrLen  = 240
)

// SeedCatalog loads the embedded models.dev snapshot into Postgres when no
// catalog exists yet or when the embedded snapshot is newer than the active
// catalog's as_of timestamp.
func SeedCatalog(ctx context.Context, owner *pgxpool.Pool) error {
	version, asOf, rows, err := snapshot.Load()
	if err != nil {
		return err
	}
	_, err = postgres.SeedCatalog(ctx, owner, version, asOf, rows)
	return err
}

// CatalogRefreshArgs is the daily River periodic job payload for refreshing
// the models.dev catalog.
type CatalogRefreshArgs struct{}

// Kind is the River job kind for catalog refresh.
func (CatalogRefreshArgs) Kind() string { return "gateway_catalog_refresh" }

// CatalogRefreshDeps provides the owner pool, optional HTTP client/URL overrides
// for tests, and notification hooks for the daily catalog refresh job.
type CatalogRefreshDeps struct {
	Owner            *pgxpool.Pool
	URL              string
	Client           *http.Client
	SettingsManagers func(ctx context.Context) ([]string, error)
	Notify           func(ctx context.Context, tx pgx.Tx, userID, title string) error
	Now              func() time.Time
	Log              *slog.Logger
}

type catalogRefreshWorker struct {
	river.WorkerDefaults[CatalogRefreshArgs]
	deps CatalogRefreshDeps
}

// AddCatalogRefresher registers the daily catalog refresh worker.
func AddCatalogRefresher(workers *river.Workers, deps CatalogRefreshDeps) {
	river.AddWorker(workers, &catalogRefreshWorker{deps: deps})
}

// CatalogRefreshInterval is the minimum interval between scheduled catalog
// refresh attempts, persisted in catalog_meta.last_attempt_at.
const CatalogRefreshInterval = 24 * time.Hour

func catalogRefreshInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		UniqueOpts: river.UniqueOpts{
			ByPeriod: time.Hour,
		},
	}
}

// CatalogRefreshPeriodic checks hourly (and on startup, deduplicated per hour)
// whether a daily catalog refresh is due; the worker itself skips when
// catalog_meta.last_attempt_at is within the last 24 hours.
func CatalogRefreshPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) {
			return CatalogRefreshArgs{}, catalogRefreshInsertOpts()
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

func (w *catalogRefreshWorker) Work(ctx context.Context, _ *river.Job[CatalogRefreshArgs]) error {
	// RefreshCatalogIfDue logs one line on failure, records last_error, and emits
	// at most one stale notice per episode; returning nil avoids River retry
	// spam on air-gapped installs.
	_ = RefreshCatalogIfDue(ctx, w.deps)
	return nil
}

// RefreshCatalogIfDue runs RefreshCatalogOnce only if catalog_meta.last_attempt_at
// is NULL or at least 24 hours old according to deps.Now, checked under the
// shared catalog advisory lock.
func RefreshCatalogIfDue(ctx context.Context, deps CatalogRefreshDeps) error {
	return refreshCatalog(ctx, deps, true)
}

// RefreshCatalogOnce fetches, validates, and atomically swaps the models.dev
// price catalog under the shared catalog advisory lock. Any failed check leaves
// the existing catalog untouched, logs one line, records last_error, and sends
// one stale-catalog notice per episode once as_of is older than 7 days.
func RefreshCatalogOnce(ctx context.Context, deps CatalogRefreshDeps) error {
	return refreshCatalog(ctx, deps, false)
}

func refreshCatalog(ctx context.Context, deps CatalogRefreshDeps, respectInterval bool) error {
	now := time.Now()
	if deps.Now != nil {
		now = deps.Now()
	}
	now = now.UTC()

	conn, locked, unlock, err := postgres.TryLockCatalog(ctx, deps.Owner)
	if err != nil {
		if deps.Log != nil {
			deps.Log.WarnContext(ctx, "catalog refresh lock failed", "err", sanitizeCatalogError(err))
		}
		return err
	}
	if !locked {
		return nil
	}
	defer unlock()

	if respectInterval {
		meta, found, err := postgres.GetCatalogMeta(ctx, conn)
		if err != nil {
			if deps.Log != nil {
				deps.Log.WarnContext(ctx, "catalog refresh meta lookup failed", "err", sanitizeCatalogError(err))
			}
			return err
		}
		if found && meta.LastAttemptAt != nil && now.Sub(meta.LastAttemptAt.UTC()) < CatalogRefreshInterval {
			return nil
		}
	}

	failRefresh := func(cause error) error {
		msg := sanitizeCatalogError(cause)
		if deps.Log != nil {
			deps.Log.WarnContext(ctx, "catalog refresh failed", "err", msg)
		}
		asOf, notifiedAt, found, rerr := postgres.RecordCatalogAttempt(ctx, conn, now, msg)
		if rerr == nil && found && notifiedAt == nil && now.Sub(asOf) > CatalogStaleAfter &&
			deps.SettingsManagers != nil && deps.Notify != nil {
			if recipients, serr := deps.SettingsManagers(ctx); serr == nil && len(recipients) > 0 {
				if tx, terr := conn.Begin(ctx); terr == nil {
					ok := true
					for _, uid := range recipients {
						if nerr := deps.Notify(ctx, tx, uid, "Model price catalog is more than 7 days old and could not be refreshed."); nerr != nil {
							ok = false
							break
						}
					}
					if ok {
						if merr := postgres.MarkCatalogStaleNotifiedTx(ctx, tx, now); merr == nil {
							_ = tx.Commit(ctx)
						} else {
							_ = tx.Rollback(ctx)
						}
					} else {
						_ = tx.Rollback(ctx)
					}
				}
			}
		}
		return cause
	}

	rawURL := strings.TrimSpace(deps.URL)
	if rawURL == "" {
		rawURL = DefaultCatalogURL
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsedURL.Scheme, "https") || parsedURL.Host == "" {
		return failRefresh(fmt.Errorf("catalog URL must use https"))
	}

	client := catalogHTTPClient(deps.Client)
	reqCtx, cancel := context.WithTimeout(ctx, CatalogFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return failRefresh(fmt.Errorf("catalog request: %w", err))
	}
	res, err := client.Do(req)
	if err != nil {
		return failRefresh(fmt.Errorf("catalog fetch: %w", err))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return failRefresh(fmt.Errorf("catalog fetch: status %d", res.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, CatalogMaxBytes+1))
	if err != nil {
		return failRefresh(fmt.Errorf("catalog read: %w", err))
	}
	if int64(len(raw)) > CatalogMaxBytes {
		return failRefresh(fmt.Errorf("catalog exceeds %d bytes", CatalogMaxBytes))
	}

	version := now.Format(time.RFC3339)
	rows, err := domain.ParseCatalog(bytes.NewReader(raw), version)
	if err != nil {
		return failRefresh(err)
	}
	prevCount, err := postgres.CatalogRowCount(ctx, conn)
	if err != nil {
		return failRefresh(err)
	}
	if err := domain.ValidateCatalog(rows, prevCount); err != nil {
		return failRefresh(err)
	}
	if err := postgres.ApplyCatalogRefreshTx(ctx, conn, version, now, rows); err != nil {
		return failRefresh(err)
	}
	if deps.Log != nil {
		deps.Log.InfoContext(ctx, "catalog refreshed", "version", version, "rows", len(rows))
	}
	return nil
}

func catalogHTTPClient(custom *http.Client) *http.Client {
	refuseRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if custom == nil {
		return &http.Client{
			Transport:     (&netguard.Guard{}).Transport(),
			Timeout:       CatalogFetchTimeout,
			CheckRedirect: refuseRedirect,
		}
	}
	cp := *custom
	if cp.Timeout <= 0 {
		cp.Timeout = CatalogFetchTimeout
	}
	cp.CheckRedirect = refuseRedirect
	return &cp
}

func sanitizeCatalogError(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > maxCatalogErrLen {
		return s[:maxCatalogErrLen]
	}
	return s
}
