package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// VerifyArgs is the River payload that runs one verification pass.
type VerifyArgs struct{}

func (VerifyArgs) Kind() string { return "audit_verify" }

type verifyWorker struct {
	river.WorkerDefaults[VerifyArgs]
	pool *pgxpool.Pool
}

// AddVerifier registers the worker that verifies the sealed chain. It runs on
// owner, the pool of the role that owns the audit tables.
func AddVerifier(workers *river.Workers, owner *pgxpool.Pool) {
	river.AddWorker(workers, &verifyWorker{pool: owner})
}

func (w *verifyWorker) Work(ctx context.Context, _ *river.Job[VerifyArgs]) error {
	return VerifyOnce(ctx, w.pool)
}

// VerifyPeriodic runs the verification at startup and then once a day.
func VerifyPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return VerifyArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

// VerifyResult is where one walk of the chain ended. Position names the first
// failing position, and Reason says how it failed.
type VerifyResult struct {
	OK       bool
	Position int64
	Reason   string
}

// VerifyQueryer is the subset of pgxpool.Pool and pgx.Tx needed to walk the
// sealed audit chain.
type VerifyQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Verify walks the sealed chain's events from position from through to in
// position order and reports the first break: a missing position is a "gap",
// a prev_hash that does not link to the event before it or a hash that does
// not cover the event's canonical bytes is a "hash" break. A to at or below
// zero means the head. The walk starts at the latest anchor, at position 1
// with the zero seed when there is none; a from above the chain start links
// from the stored hash of the event before it when that event is still
// retained, and from the chain start when it is not. Only a database failure
// returns an error.
func Verify(ctx context.Context, pool *pgxpool.Pool, from, to int64) (VerifyResult, error) {
	return verify(ctx, pool, from, to)
}

// VerifyTx walks the sealed chain's events inside an open transaction.
func VerifyTx(ctx context.Context, tx pgx.Tx, from, to int64) (VerifyResult, error) {
	return verify(ctx, tx, from, to)
}

func verify(ctx context.Context, q VerifyQueryer, from, to int64) (VerifyResult, error) {
	var anchor int64
	var prevHash []byte
	err := q.QueryRow(ctx,
		`SELECT position, hash FROM audit.anchors ORDER BY position DESC LIMIT 1`,
	).Scan(&anchor, &prevHash)
	if errors.Is(err, pgx.ErrNoRows) {
		anchor, prevHash = 0, make([]byte, 32)
	} else if err != nil {
		return VerifyResult{}, fmt.Errorf("audit: anchor: %w", err)
	}
	// The chain of what remains starts one past the anchor.
	start := anchor + 1
	if from > start {
		// The window opens mid-chain: the event before it holds the hash the
		// walk links from. A purge may have taken that event, in which case
		// the walk covers everything that remains from the chain start.
		var hash []byte
		err := q.QueryRow(ctx,
			`SELECT hash FROM audit.events WHERE position = $1`, from-1,
		).Scan(&hash)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			from = start
		case err != nil:
			return VerifyResult{}, fmt.Errorf("audit: link hash: %w", err)
		default:
			prevHash = hash
		}
	} else {
		from = start
	}
	if to <= 0 {
		if err := q.QueryRow(ctx,
			`SELECT position FROM audit.head WHERE one`,
		).Scan(&to); err != nil {
			return VerifyResult{}, fmt.Errorf("audit: head: %w", err)
		}
	}
	if from > to {
		return VerifyResult{OK: true}, nil
	}

	rows, err := q.Query(ctx, `
		SELECT position, prev_hash, hash,
		       id::text, occurred_at, service, action, outcome, reason,
		       actor_type, COALESCE(actor_id::text, ''), COALESCE(actor_role_id::text, ''),
		       credential_type, COALESCE(credential_id::text, ''),
		       target_type, target_id, changes,
		       COALESCE(request_id::text, ''), COALESCE(host(source_ip), ''), user_agent
		FROM audit.events
		WHERE position >= $1 AND position <= $2
		ORDER BY position`, from, to)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("audit: events: %w", err)
	}
	defer rows.Close()

	next := from
	for rows.Next() {
		var position int64
		var prev, hash []byte
		var event Row
		var changes []byte
		if err := rows.Scan(
			&position, &prev, &hash,
			&event.ID, &event.OccurredAt, &event.Service, &event.Action,
			&event.Outcome, &event.Reason,
			&event.ActorType, &event.ActorID, &event.ActorRoleID,
			&event.CredentialType, &event.CredentialID,
			&event.TargetType, &event.TargetID, &changes,
			&event.RequestID, &event.SourceIP, &event.UserAgent,
		); err != nil {
			return VerifyResult{}, fmt.Errorf("audit: event scan: %w", err)
		}
		if err := json.Unmarshal(changes, &event.Changes); err != nil {
			return VerifyResult{}, fmt.Errorf("audit: event changes: %w", err)
		}
		if position != next {
			return VerifyResult{Position: next, Reason: "gap"}, nil
		}
		// The hash signs the stored prev_hash and the row read back in the
		// same shape the sealer scans, so a changed column breaks it.
		recomputed := sha256.Sum256(append(append([]byte{}, prev...), Canonical(event)...))
		if !bytes.Equal(prev, prevHash) || !bytes.Equal(hash, recomputed[:]) {
			return VerifyResult{Position: position, Reason: "hash"}, nil
		}
		next, prevHash = position+1, hash
	}
	if err := rows.Err(); err != nil {
		return VerifyResult{}, fmt.Errorf("audit: events: %w", err)
	}
	if next <= to {
		// The walk ran out of events before to: every position from next on
		// is missing.
		return VerifyResult{Position: next, Reason: "gap"}, nil
	}
	return VerifyResult{OK: true}, nil
}

// VerifyOnce runs one pass of the daily worker: it walks the chain from just
// after the last successful verification to the head and records where the
// walk ended. With nothing new to walk it records nothing, and a failure
// leaves the last success in place, so the next pass walks the same range
// again.
func VerifyOnce(ctx context.Context, owner *pgxpool.Pool) error {
	last, err := lastVerified(ctx, owner)
	if err != nil {
		return err
	}
	var head int64
	if err := owner.QueryRow(ctx,
		`SELECT position FROM audit.head WHERE one`,
	).Scan(&head); err != nil {
		return fmt.Errorf("audit: head: %w", err)
	}
	if head <= last {
		return nil
	}

	result, err := Verify(ctx, owner, last+1, head)
	if err != nil {
		return err
	}
	fields := []string{"from_position", "through_position"}
	values := map[string]Change{
		"from_position":    {To: last + 1},
		"through_position": {To: head},
	}
	outcome := Success
	if !result.OK {
		outcome = Failure
		fields = append(fields, "failed_position")
		values["failed_position"] = Change{To: result.Position}
		slog.ErrorContext(ctx, "audit chain verification failed",
			"position", result.Position, "reason", result.Reason)
	}
	if err := Record(WithRunner(ctx, CredentialService), owner, Event{
		Action:  AuditVerify,
		Outcome: outcome,
		Changes: Changes{Fields: fields, Values: values},
	}); err != nil {
		return fmt.Errorf("audit: record the verification: %w", err)
	}
	return nil
}

// lastVerified returns the highest position a successful verification reported
// reaching, sealed or still pending: the recorded event reaches the head it
// walked through, so a quiet state verifies nothing new until the sealer adds
// events again. Zero means nothing was verified yet.
func lastVerified(ctx context.Context, owner *pgxpool.Pool) (int64, error) {
	var last int64
	if err := owner.QueryRow(ctx, `
		SELECT COALESCE(max(through), 0) FROM (
			SELECT (changes -> 'values' -> 'through_position' ->> 'to')::bigint AS through
			FROM audit.events WHERE action = 'audit.verify' AND outcome = 'success'
			UNION ALL
			SELECT (changes -> 'values' -> 'through_position' ->> 'to')::bigint
			FROM audit.pending WHERE action = 'audit.verify' AND outcome = 'success'
		) verified`).Scan(&last); err != nil {
		return 0, fmt.Errorf("audit: last verification: %w", err)
	}
	return last, nil
}
