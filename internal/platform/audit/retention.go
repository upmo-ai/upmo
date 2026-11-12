package audit

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// RetainAhead is how many monthly partitions exist beyond the current one,
// so a sealed event always finds the partition of its sealed time.
const RetainAhead = 2

// RetentionArgs is the River payload that runs one retention pass.
type RetentionArgs struct{}

func (RetentionArgs) Kind() string { return "audit_retention" }

type retentionWorker struct {
	river.WorkerDefaults[RetentionArgs]
	pool          *pgxpool.Pool
	retentionDays int
}

// AddRetention registers the worker that keeps the audit partitions. It runs
// on owner, the pool of the role that owns the audit tables, and keeps
// retentionDays of sealed events.
func AddRetention(workers *river.Workers, owner *pgxpool.Pool, retentionDays int) {
	river.AddWorker(workers, &retentionWorker{pool: owner, retentionDays: retentionDays})
}

func (w *retentionWorker) Work(ctx context.Context, _ *river.Job[RetentionArgs]) error {
	return Retain(ctx, w.pool, time.Now(), w.retentionDays)
}

// RetentionPeriodic runs the retention pass at startup and then once a day.
func RetentionPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return RetentionArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

// monthlyPartition matches the name of one monthly partition, like
// events_y2026m11. The default partition does not match.
var monthlyPartition = regexp.MustCompile(`^events_y(\d{4})m(\d{2})$`)

// expiredPartition is one expired monthly partition up for purging: its table
// name and the first instant it no longer covers, the first day of the next
// month.
type expiredPartition struct {
	name    string
	through time.Time
}

// Retain keeps the monthly partitions of audit.events in step with now: the
// current month and the RetainAhead after it exist, every whole month that
// ended at or before now minus retentionDays days is dropped, and events
// parked in the default partition are logged. A dropped partition with events
// leaves one anchor at its highest position first, so the chain of what
// remains starts at a verifiable hash; no event row is ever removed on its
// own.
func Retain(ctx context.Context, owner *pgxpool.Pool, now time.Time, retentionDays int) error {
	now = now.UTC()
	month := firstOfMonth(now)
	for i := 0; i <= RetainAhead; i++ {
		if err := createPartition(ctx, owner, month.AddDate(0, i, 0)); err != nil {
			return err
		}
	}
	if err := logDefaultPartition(ctx, owner); err != nil {
		return err
	}
	expired, err := expiredPartitions(ctx, owner, now.AddDate(0, 0, -retentionDays))
	if err != nil {
		return err
	}
	for _, part := range expired {
		events, position, err := dropPartition(ctx, owner, part)
		if err != nil {
			return err
		}
		if err := Record(WithRunner(ctx, CredentialService), owner, Event{
			Action:   AuditPurge,
			Outcome:  Success,
			TargetID: part.name,
			Changes: Changes{
				Fields: []string{"partition", "events", "through_position"},
				Values: map[string]Change{
					"partition":        {To: part.name},
					"events":           {To: events},
					"through_position": {To: position},
				},
			},
		}); err != nil {
			return fmt.Errorf("audit: record the purge of %s: %w", part.name, err)
		}
	}
	return nil
}

// firstOfMonth returns midnight UTC of the first day of t's month.
func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// partitionName is the name of month's partition, like events_y2026m11.
func partitionName(month time.Time) string {
	return fmt.Sprintf("events_y%04dm%02d", month.Year(), int(month.Month()))
}

// createPartition ensures the monthly partition of month exists, with the
// bounds its name states.
func createPartition(ctx context.Context, owner *pgxpool.Pool, month time.Time) error {
	name := partitionName(month)
	// The name and both bounds come from the month itself, never from a
	// caller, so the statement is as safe as the migration's own.
	stmt := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS audit.%s PARTITION OF audit.events FOR VALUES FROM ('%s') TO ('%s')",
		name, month.Format("2006-01-02"), month.AddDate(0, 1, 0).Format("2006-01-02"))
	if _, err := owner.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("audit: create partition %s: %w", name, err)
	}
	return nil
}

// logDefaultPartition logs the events that landed in the default partition
// because no monthly partition covered their sealed time. They stay stored
// there; the log says where to look.
func logDefaultPartition(ctx context.Context, owner *pgxpool.Pool) error {
	var parked int64
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.events_default`).Scan(&parked); err != nil {
		return fmt.Errorf("audit: count the default partition: %w", err)
	}
	if parked > 0 {
		slog.ErrorContext(ctx, "audit events are parked in the default partition", "events", parked)
	}
	return nil
}

// expiredPartitions lists the monthly partitions whose whole month ended at
// or before cutoff, oldest first. The default partition never matches the
// monthly name and is never dropped.
func expiredPartitions(ctx context.Context, owner *pgxpool.Pool, cutoff time.Time) ([]expiredPartition, error) {
	rows, err := owner.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits AS i
		JOIN pg_class AS c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'audit.events'::regclass`)
	if err != nil {
		return nil, fmt.Errorf("audit: list partitions: %w", err)
	}
	defer rows.Close()
	var expired []expiredPartition
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("audit: read a partition name: %w", err)
		}
		parts := monthlyPartition.FindStringSubmatch(name)
		if parts == nil {
			continue
		}
		year, _ := strconv.Atoi(parts[1])
		mon, _ := strconv.Atoi(parts[2])
		if mon < 1 || mon > 12 {
			continue
		}
		through := time.Date(year, time.Month(mon), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		if through.After(cutoff) {
			continue
		}
		expired = append(expired, expiredPartition{name: name, through: through})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: list partitions: %w", err)
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].through.Before(expired[j].through) })
	return expired, nil
}

// dropPartition drops one expired partition in a single transaction. When it
// holds events, the anchor naming its highest position and that event's hash
// is written first; an empty partition drops with no anchor. It returns how
// many events the partition held and the position its anchor names, zero
// when it wrote none.
func dropPartition(ctx context.Context, owner *pgxpool.Pool, part expiredPartition) (int64, int64, error) {
	tx, err := owner.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var events int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM audit."+part.name).Scan(&events); err != nil {
		return 0, 0, fmt.Errorf("audit: count %s: %w", part.name, err)
	}
	var position int64
	if events > 0 {
		var hash []byte
		if err := tx.QueryRow(ctx,
			"SELECT position, hash FROM audit."+part.name+" ORDER BY position DESC LIMIT 1",
		).Scan(&position, &hash); err != nil {
			return 0, 0, fmt.Errorf("audit: read the last event of %s: %w", part.name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO audit.anchors (position, hash, purged_before) VALUES ($1, $2, $3)`,
			position, hash, part.through); err != nil {
			return 0, 0, fmt.Errorf("audit: anchor %s: %w", part.name, err)
		}
	}
	if _, err := tx.Exec(ctx, "DROP TABLE audit."+part.name); err != nil {
		return 0, 0, fmt.Errorf("audit: drop %s: %w", part.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("audit: commit the purge of %s: %w", part.name, err)
	}
	return events, position, nil
}
