package audit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// SealBatch is how many pending events one sealing pass takes at most. A pass
// that filled the batch repeats until the pending table is empty.
const SealBatch = 1000

// SealArgs is the River payload that runs one sealing pass.
type SealArgs struct{}

func (SealArgs) Kind() string { return "audit_seal" }

type sealWorker struct {
	river.WorkerDefaults[SealArgs]
	pool *pgxpool.Pool
	out  io.Writer
}

// AddSealer registers the worker that seals pending events. It runs on owner,
// the pool of the role that owns the audit tables, and writes the OCSF stream
// to out.
func AddSealer(workers *river.Workers, owner *pgxpool.Pool, out io.Writer) {
	river.AddWorker(workers, &sealWorker{pool: owner, out: out})
}

func (w *sealWorker) Work(ctx context.Context, _ *river.Job[SealArgs]) error {
	for {
		sealed, err := SealOnce(ctx, w.pool, w.out)
		if err != nil {
			return err
		}
		if sealed < SealBatch {
			return nil
		}
	}
}

// SealPeriodic runs the sealer at startup and then every two seconds.
func SealPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(2*time.Second),
		func() (river.JobArgs, *river.InsertOpts) { return SealArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

// sealedEvent is one pending row with the chain bytes the pass gave it.
type sealedEvent struct {
	row       Row
	canonical []byte
	prev      []byte
	hash      []byte
}

// SealOnce moves the next batch of pending events into the sealed table in
// order: each gets a gapless position, a hash chained to the previous event,
// and one OCSF line on out, then the batch commits. The line precedes the
// commit, so the stream is at least once and consumers de-duplicate on the
// event id. It returns how many events were sealed.
func SealOnce(ctx context.Context, owner *pgxpool.Pool, out io.Writer) (int, error) {
	tx, err := owner.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var position int64
	var prevHash []byte
	var sealed time.Time
	// The head starts at -infinity, the migration's seed, which time.Time
	// cannot hold. Clamping it in SQL keeps every value the scan sees finite,
	// and the GREATEST(clock_timestamp(), $2) on the inserts below still
	// seals each event at or after the head's sealed time.
	if err := tx.QueryRow(ctx,
		`SELECT position, hash, GREATEST(sealed_at, clock_timestamp()) FROM audit.head WHERE one FOR UPDATE`,
	).Scan(&position, &prevHash, &sealed); err != nil {
		return 0, fmt.Errorf("audit: head: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id::text, occurred_at, service, action, outcome, reason,
		       actor_type, COALESCE(actor_id::text, ''), COALESCE(actor_role_id::text, ''),
		       credential_type, COALESCE(credential_id::text, ''),
		       target_type, target_id, changes,
		       COALESCE(request_id::text, ''), COALESCE(host(source_ip), ''), user_agent
		FROM audit.pending
		ORDER BY id
		LIMIT $1`, SealBatch)
	if err != nil {
		return 0, fmt.Errorf("audit: pending: %w", err)
	}

	batch := make([]sealedEvent, 0, SealBatch)
	ids := make([]string, 0, SealBatch)
	for rows.Next() {
		var event sealedEvent
		var changes []byte
		if err := rows.Scan(
			&event.row.ID, &event.row.OccurredAt, &event.row.Service, &event.row.Action,
			&event.row.Outcome, &event.row.Reason,
			&event.row.ActorType, &event.row.ActorID, &event.row.ActorRoleID,
			&event.row.CredentialType, &event.row.CredentialID,
			&event.row.TargetType, &event.row.TargetID, &changes,
			&event.row.RequestID, &event.row.SourceIP, &event.row.UserAgent,
		); err != nil {
			rows.Close()
			return 0, fmt.Errorf("audit: pending scan: %w", err)
		}
		if err := json.Unmarshal(changes, &event.row.Changes); err != nil {
			rows.Close()
			return 0, fmt.Errorf("audit: pending changes: %w", err)
		}
		event.canonical = Canonical(event.row)
		event.prev = prevHash
		hash := sha256.Sum256(append(append([]byte{}, prevHash...), event.canonical...))
		event.hash = hash[:]
		prevHash = event.hash
		batch = append(batch, event)
		ids = append(ids, event.row.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("audit: pending: %w", err)
	}

	for _, event := range batch {
		position++
		var sealedAt time.Time
		// The changes column keeps the event's changes, the form Canonical
		// signs, so a row read back from audit.events hashes to the stored
		// hash again. The values came from JSON, so Marshal cannot fail.
		changes, _ := json.Marshal(normalChanges(event.row.Changes))
		// The single sealer sets sealed_at, so it never goes backwards and
		// agrees with position; the chain hash covers occurred_at instead.
		if err := tx.QueryRow(ctx, `
			INSERT INTO audit.events (
				position, sealed_at, prev_hash, hash,
				id, occurred_at, service, action, outcome, reason,
				actor_type, actor_id, actor_role_id, credential_type, credential_id,
				target_type, target_id, changes, request_id, source_ip, user_agent
			) VALUES (
				$1, GREATEST(clock_timestamp(), $2), $3, $4,
				$5::uuid, $6, $7, $8, $9, $10,
				$11, NULLIF($12, '')::uuid, NULLIF($13, '')::uuid, $14, NULLIF($15, '')::uuid,
				$16, $17, $18::jsonb, NULLIF($19, '')::uuid, NULLIF($20, '')::inet, $21
			)
			RETURNING sealed_at`,
			position, sealed, event.prev, event.hash,
			event.row.ID, event.row.OccurredAt, event.row.Service, event.row.Action,
			event.row.Outcome, event.row.Reason,
			event.row.ActorType, event.row.ActorID, event.row.ActorRoleID,
			event.row.CredentialType, event.row.CredentialID,
			event.row.TargetType, event.row.TargetID, changes,
			event.row.RequestID, event.row.SourceIP, event.row.UserAgent,
		).Scan(&sealedAt); err != nil {
			return 0, fmt.Errorf("audit: seal %s: %w", event.row.ID, err)
		}
		sealed = sealedAt

		line, err := OCSF(Sealed{Row: event.row, Position: position, PrevHash: event.prev, Hash: event.hash})
		if err != nil {
			return 0, err
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			return 0, fmt.Errorf("audit: stream: %w", err)
		}
	}

	if len(batch) > 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM audit.pending WHERE id = ANY($1::uuid[])`, ids,
		); err != nil {
			return 0, fmt.Errorf("audit: pending delete: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE audit.head SET position = $1, hash = $2, sealed_at = $3 WHERE one`,
			position, prevHash, sealed,
		); err != nil {
			return 0, fmt.Errorf("audit: head update: %w", err)
		}
		logLostPartitions(ctx, tx, position-int64(len(batch)), position)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("audit: seal commit: %w", err)
	}
	return len(batch), nil
}

// logLostPartitions notes the events of one pass that landed in the default
// partition because no monthly partition covered their sealed time. They stay
// stored there.
func logLostPartitions(ctx context.Context, tx pgx.Tx, first, last int64) {
	rows, err := tx.Query(ctx, `
		SELECT position FROM audit.events
		WHERE position > $1 AND position <= $2
		  AND tableoid = 'audit.events_default'::regclass`, first, last)
	if err != nil {
		slog.ErrorContext(ctx, "audit default partition check failed", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var position int64
		if err := rows.Scan(&position); err != nil {
			slog.ErrorContext(ctx, "audit default partition scan failed", "err", err)
			return
		}
		slog.ErrorContext(ctx, "audit event landed in the default partition", "position", position)
	}
}
