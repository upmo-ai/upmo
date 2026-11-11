// Package audit records append-only security-relevant changes. It is platform
// code, not a bounded context: contexts record into it, never the reverse.
package audit

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/migrate"
)

// Schema is the Postgres schema for pending and sealed audit events.
const Schema = "audit"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this package's Goose migration files.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("audit: embed migrations: " + err.Error())
	}
	return files
}

// Up applies Goose migrations in schema audit.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		return fmt.Errorf("audit: goose: %w", err)
	}
	return nil
}

// Action is one entry of the closed catalog, for example "auth.sign_out".
type Action string

// Outcome is how a recorded change ended.
type Outcome string

// Outcomes and the action that fires once per startup.
const (
	Success Outcome = "success"
	Denied  Outcome = "denied"
	Failure Outcome = "failure"

	SystemMigrated Action = "system.migrated"
)

// Actor is who acted. ID stays empty unless Type is user, and RoleID holds the
// role the actor held at that moment.
type Actor struct {
	Type           string
	ID             string
	RoleID         string
	CredentialType string
	CredentialID   string
}

// Change is one field's value before and after. Either side may be nil.
type Change struct {
	From any `json:"from"`
	To   any `json:"to"`
}

// Changes holds the changed field names, and values only for the fields the
// action's safe list admits.
type Changes struct {
	Fields []string          `json:"fields"`
	Values map[string]Change `json:"values"`
}

// Event is one audit record as the caller states it. Actor, request id,
// address and user agent come from the context.
type Event struct {
	Action     Action
	Outcome    Outcome
	Reason     string
	TargetType string
	TargetID   string
	Changes    Changes
}

// Row is a stored event: everything the hash chain covers.
type Row struct {
	ID             string
	OccurredAt     time.Time
	Service        string
	Action         Action
	Outcome        Outcome
	Reason         string
	ActorType      string
	ActorID        string
	ActorRoleID    string
	CredentialType string
	CredentialID   string
	TargetType     string
	TargetID       string
	Changes        Changes
	RequestID      string
	SourceIP       string
	UserAgent      string
}

// Execer is the handle Record writes through: the business transaction when
// there is one, so the event and the change share a commit.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type definition struct {
	target   string
	class    int
	activity int
	values   map[string]bool
}

var catalog = map[Action]definition{}

// register admits one action to the catalog with its target type, its OCSF
// class and activity, and the change fields whose values may be stored.
func register(action Action, target string, class, activity int, values ...string) {
	safe := make(map[string]bool, len(values))
	for _, field := range values {
		safe[field] = true
	}
	catalog[action] = definition{target: target, class: class, activity: activity, values: safe}
}

// scope is what one request contributes to its events. The middleware starts
// it; WithActor and Annotate fill it; Record reads it.
type scope struct {
	service   string
	actor     Actor
	requestID string
	sourceIP  string
	userAgent string
	annotated Event
	recorded  bool
}

type scopeKey struct{}

// WithActor marks who acts. The session and token middleware call it once the
// caller is known.
func WithActor(ctx context.Context, actor Actor) {
	if s, ok := ctx.Value(scopeKey{}).(*scope); ok {
		s.actor = actor
	}
}

// Annotate adds a reason or a target to the event the middleware writes for a
// refused or failed request.
func Annotate(ctx context.Context, event Event) {
	if s, ok := ctx.Value(scopeKey{}).(*scope); ok {
		if event.Reason != "" {
			s.annotated.Reason = event.Reason
		}
		if event.TargetType != "" {
			s.annotated.TargetType = event.TargetType
		}
		if event.TargetID != "" {
			s.annotated.TargetID = event.TargetID
		}
	}
}

// system is the actor behind events with no request, like the startup event.
var system = Actor{Type: "system", CredentialType: "none"}

// Record stores one event in the caller's transaction. An action outside the
// catalog is refused, and change values off the action's safe list are
// dropped, keeping only the field name.
func Record(ctx context.Context, q Execer, event Event) error {
	def, ok := catalog[event.Action]
	if !ok {
		return fmt.Errorf("audit: action %q is not in the catalog", event.Action)
	}
	if event.TargetType == "" {
		event.TargetType = def.target
	}
	event.Changes = filtered(event.Changes, def)

	actor := system
	service := "brain"
	requestID, sourceIP, userAgent := "", "", ""
	if s, ok := ctx.Value(scopeKey{}).(*scope); ok {
		if s.actor.Type != "" {
			actor = s.actor
		}
		if s.service != "" {
			service = s.service
		}
		requestID, sourceIP, userAgent = s.requestID, s.sourceIP, s.userAgent
	}

	encoded, err := json.Marshal(normalChanges(event.Changes))
	if err != nil {
		return fmt.Errorf("audit: encode changes: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO audit.pending (
			service, action, outcome, reason,
			actor_type, actor_id, actor_role_id, credential_type, credential_id,
			target_type, target_id, changes, request_id, source_ip, user_agent
		) VALUES (
			$1, $2, $3, $4,
			$5, NULLIF($6, '')::uuid, NULLIF($7, '')::uuid, $8, NULLIF($9, '')::uuid,
			$10, $11, $12::jsonb, NULLIF($13, '')::uuid, NULLIF($14, '')::inet, $15
		)`,
		service, event.Action, event.Outcome, event.Reason,
		actor.Type, actor.ID, actor.RoleID, actor.CredentialType, actor.CredentialID,
		event.TargetType, event.TargetID, encoded, requestID, sourceIP, userAgent,
	); err != nil {
		return err
	}
	if s, ok := ctx.Value(scopeKey{}).(*scope); ok {
		s.recorded = true
	}
	return nil
}

// filtered sorts the field names, keeps values only for safe fields, and adds
// each surviving value's field name to the list.
func filtered(changes Changes, def definition) Changes {
	fields := append([]string(nil), changes.Fields...)
	values := make(map[string]Change, len(changes.Values))
	for field, change := range changes.Values {
		if !def.values[field] {
			continue
		}
		values[field] = change
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return Changes{Fields: slices.Compact(fields), Values: values}
}

// normalChanges makes nil fields and values explicit, so neither the stored
// nor the canonical form holds a null for them.
func normalChanges(changes Changes) Changes {
	if changes.Fields == nil {
		changes.Fields = []string{}
	}
	if changes.Values == nil {
		changes.Values = map[string]Change{}
	}
	return changes
}

// Canonical returns the bytes the hash chain signs: the event as one JSON
// object with sorted keys, no insignificant whitespace, and absent values as
// empty strings. The sealer and the verifier hash this same form.
func Canonical(row Row) []byte {
	value := map[string]any{
		"id":              row.ID,
		"occurred_at":     row.OccurredAt.UTC().Format(time.RFC3339Nano),
		"service":         row.Service,
		"action":          string(row.Action),
		"outcome":         string(row.Outcome),
		"reason":          row.Reason,
		"actor_type":      row.ActorType,
		"actor_id":        row.ActorID,
		"actor_role_id":   row.ActorRoleID,
		"credential_type": row.CredentialType,
		"credential_id":   row.CredentialID,
		"target_type":     row.TargetType,
		"target_id":       row.TargetID,
		"changes":         normalChanges(row.Changes),
		"request_id":      row.RequestID,
		"source_ip":       row.SourceIP,
		"user_agent":      row.UserAgent,
	}
	// The values above are all JSON-safe, so Marshal cannot fail here.
	bytes, _ := json.Marshal(value)
	return bytes
}

// SchemaVersions lists the applied migration version of every schema that
// keeps a Goose version table, as sorted "schema=version" pairs.
func SchemaVersions(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	rows, err := pool.Query(ctx, `
		SELECT table_schema
		FROM information_schema.tables
		WHERE table_name = 'goose_db_version'
		  AND table_schema NOT IN ('pg_catalog', 'information_schema')
		ORDER BY table_schema`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			return "", err
		}
		var version int64
		// The schema name is read from the catalog and quoted as an
		// identifier, never interpolated as a value.
		query := "SELECT max(version_id) FROM " + pgx.Identifier{schema}.Sanitize() + ".goose_db_version"
		if err := pool.QueryRow(ctx, query).Scan(&version); err != nil {
			return "", err
		}
		out = append(out, schema+"="+strconv.FormatInt(version, 10))
	}
	return strings.Join(out, " "), rows.Err()
}

// Migrated records system.migrated with the schema versions as its safe
// value. It runs after the migrations, on the owner pool.
func Migrated(ctx context.Context, owner *pgxpool.Pool) error {
	versions, err := SchemaVersions(ctx, owner)
	if err != nil {
		return fmt.Errorf("audit: schema versions: %w", err)
	}
	return Record(ctx, owner, Event{
		Action:  SystemMigrated,
		Outcome: Success,
		Changes: Changes{
			Fields: []string{"versions"},
			Values: map[string]Change{"versions": {To: versions}},
		},
	})
}
