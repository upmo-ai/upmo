// Reading the sealed trail.
//
// An operator pulls events out of an install with a personal token that holds
// the audit:view scope:
//
//  1. Sign in and create the token: POST /api/tokens with
//     {"name":"siem","scope":["audit:view"],"days":90}. The secret is shown
//     once.
//  2. Poll GET /api/audit/events?limit=1000 with "Authorization: Bearer
//     <secret>", starting at after=0.
//  3. Take next from each answer and ask again with after=<next> until more
//     is false.
//  4. De-duplicate the lines on metadata.uid: a retried poll fetches the same
//     window again, and a restart may record one extra audit.export.
//
// Every line is the OCSF object the sealer wrote, in position order, and the
// positions are gapless.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/httpserver"
)

// How many events one answer holds without a limit, and the most it may hold.
const (
	readDefaultLimit = 200
	readMaxLimit     = 1000
)

// Reader is the caller a guard resolved for the events endpoint: a person
// through a session, or the same person through a token narrowed to its
// scope. Allow is the caller's permission names, comma-separated, the form
// the row policy reads.
type Reader struct {
	UserID         string
	RoleID         string
	CredentialType string
	CredentialID   string
	Allow          string
}

type readerKey struct{}

// WithReader stores the caller a guard resolved for the events endpoint.
func WithReader(ctx context.Context, reader Reader) context.Context {
	return context.WithValue(ctx, readerKey{}, reader)
}

// ReaderFrom retrieves the caller the guard resolved.
func ReaderFrom(ctx context.Context) (Reader, bool) {
	reader, ok := ctx.Value(readerKey{}).(Reader)
	return reader, ok
}

// ReadHandler serves GET /api/audit/events: the sealed trail in position
// order, each event exactly as the sealer wrote it. The guard resolves the
// caller and refuses anyone without audit:view; the read itself runs as the
// app role with the caller's allow list set for the row policy, a second line
// of defense behind the guard.
type ReadHandler struct {
	// Pool is the request pool, which runs as upmo_app.
	Pool *pgxpool.Pool
	// Now is the clock of the hourly export window. Nil follows time.Now.
	Now func() time.Time

	mu      sync.Mutex
	exports map[string]time.Time
}

func (h *ReadHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// exportDue reports whether the hourly export slot of one token is open. The
// slot is taken only after the export commits, so a rolled-back or failed
// read leaves the hour open, and two reads of one token that overlap may each
// record one. A restart forgets the reservations, so it may record one extra
// export.
func (h *ReadHandler) exportDue(tokenID string, at time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if last, ok := h.exports[tokenID]; ok && at.Sub(last) < time.Hour {
		return false
	}
	return true
}

// exportDone takes the token's hourly export slot, once its export committed.
func (h *ReadHandler) exportDone(tokenID string, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exports == nil {
		h.exports = make(map[string]time.Time)
	}
	h.exports[tokenID] = at
}

// Mount registers the events route. guard runs first: it resolves the caller,
// names them on the audit scope and refuses anyone without audit:view.
func (h *ReadHandler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/audit/events", wrap(http.HandlerFunc(h.events)))
}

// eventsAnswer is one page of the trail: the OCSF bytes of each event, the
// position to ask for after, and whether another page follows.
type eventsAnswer struct {
	Events []json.RawMessage `json:"events"`
	Next   int64             `json:"next"`
	More   bool              `json:"more"`
}

func (h *ReadHandler) events(w http.ResponseWriter, r *http.Request) {
	reader, ok := ReaderFrom(r.Context())
	if !ok {
		// The guard resolves a reader before the handler runs.
		http.Error(w, "audit", http.StatusInternalServerError)
		return
	}
	query, err := parseReadQuery(r.URL.Query())
	if err != nil {
		httpserver.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	answer, err := h.read(r.Context(), reader, query)
	if err != nil {
		http.Error(w, "audit", http.StatusInternalServerError)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, answer)
}

// read runs one page in a single transaction: the caller's allow list opens
// the row policy, the query reads one event beyond the limit to decide more,
// and the read itself is audited before the commit, so a failed audit insert
// fails the answer too.
func (h *ReadHandler) read(ctx context.Context, reader Reader, query readQuery) (eventsAnswer, error) {
	// The export window is decided here and left open until the export
	// commits, so a read that fails or rolls back consumes no hour.
	at := h.now()
	export := reader.CredentialType == "token" && h.exportDue(reader.CredentialID, at)
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		return eventsAnswer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('upmo.allow', $1, true), set_config('upmo.user_id', $2, true)`,
		reader.Allow, reader.UserID); err != nil {
		return eventsAnswer{}, err
	}

	events, next, more, err := queryPage(ctx, tx, query)
	if err != nil {
		return eventsAnswer{}, err
	}

	// Reading is audited: a person's read records every time, a token's
	// export at most once an hour, so a polling collector cannot fill the
	// trail it reads.
	if reader.CredentialType == "token" {
		if !export {
			NoChange(ctx)
		} else if err := Record(ctx, tx, Event{
			Action:  AuditExport,
			Outcome: Success,
			Changes: Changes{Values: map[string]Change{"count": {To: len(events)}}},
		}); err != nil {
			return eventsAnswer{}, err
		}
	} else if err := Record(ctx, tx, Event{
		Action:  AuditRead,
		Outcome: Success,
		Changes: Changes{Values: map[string]Change{
			"filters": {To: query.filters},
			"count":   {To: len(events)},
		}},
	}); err != nil {
		return eventsAnswer{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return eventsAnswer{}, err
	}
	if export {
		h.exportDone(reader.CredentialID, at)
	}
	return eventsAnswer{Events: events, Next: next, More: more}, nil
}

// queryPage reads one page of sealed events in the shape the sealer scans and
// maps each to its OCSF bytes.
func queryPage(ctx context.Context, tx pgx.Tx, query readQuery) ([]json.RawMessage, int64, bool, error) {
	where, args := query.where()
	rows, err := tx.Query(ctx, `
		SELECT position, prev_hash, hash,
		       id::text, occurred_at, service, action, outcome, reason,
		       actor_type, COALESCE(actor_id::text, ''), COALESCE(actor_role_id::text, ''),
		       credential_type, COALESCE(credential_id::text, ''),
		       target_type, target_id, changes,
		       COALESCE(request_id::text, ''), COALESCE(host(source_ip), ''), user_agent
		FROM audit.events
		WHERE `+where+`
		ORDER BY position
		LIMIT `+strconv.Itoa(query.limit+1), args...)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()

	events := make([]json.RawMessage, 0, query.limit)
	next := query.after
	var more bool
	for rows.Next() {
		var sealed Sealed
		var changes []byte
		if err := rows.Scan(
			&sealed.Position, &sealed.PrevHash, &sealed.Hash,
			&sealed.ID, &sealed.OccurredAt, &sealed.Service, &sealed.Action,
			&sealed.Outcome, &sealed.Reason,
			&sealed.ActorType, &sealed.ActorID, &sealed.ActorRoleID,
			&sealed.CredentialType, &sealed.CredentialID,
			&sealed.TargetType, &sealed.TargetID, &changes,
			&sealed.RequestID, &sealed.SourceIP, &sealed.UserAgent,
		); err != nil {
			return nil, 0, false, err
		}
		// The extra row only says another page follows.
		if len(events) == query.limit {
			more = true
			break
		}
		if err := json.Unmarshal(changes, &sealed.Changes); err != nil {
			return nil, 0, false, err
		}
		line, err := OCSF(sealed)
		if err != nil {
			return nil, 0, false, err
		}
		events = append(events, line)
		next = sealed.Position
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	return events, next, more, nil
}

// readQuery is one parsed request for sealed events: a page after a position,
// narrowed by the filters the caller set. filters holds the names of the
// filters used, never their values, so the audit event cannot leak them.
type readQuery struct {
	after      int64
	limit      int
	action     string
	actorID    string
	targetType string
	targetID   string
	outcome    string
	from       time.Time
	to         time.Time
	filters    []string
}

// parseReadQuery reads after, limit and the filters from one request's query.
// A malformed or out-of-range value is a client error.
func parseReadQuery(values url.Values) (readQuery, error) {
	query := readQuery{limit: readDefaultLimit}
	if raw := values.Get("after"); raw != "" {
		after, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			return readQuery{}, errors.New("after must be a position from 0")
		}
		query.after = after
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > readMaxLimit {
			return readQuery{}, errors.New("limit must be a whole number from 1 to 1000")
		}
		query.limit = limit
	}
	text := func(name string, field *string) {
		if raw := values.Get(name); raw != "" {
			*field = raw
			query.filters = append(query.filters, name)
		}
	}
	text("action", &query.action)
	if raw := values.Get("actor_id"); raw != "" {
		if _, err := uuid.Parse(raw); err != nil {
			return readQuery{}, errors.New("actor_id must be an id")
		}
		query.actorID = raw
		query.filters = append(query.filters, "actor_id")
	}
	text("target_type", &query.targetType)
	text("target_id", &query.targetID)
	text("outcome", &query.outcome)
	moment := func(name string, field *time.Time) error {
		raw := values.Get(name)
		if raw == "" {
			return nil
		}
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return errors.New(name + " must be an RFC 3339 timestamp")
		}
		*field = at
		query.filters = append(query.filters, name)
		return nil
	}
	if err := moment("from", &query.from); err != nil {
		return readQuery{}, err
	}
	if err := moment("to", &query.to); err != nil {
		return readQuery{}, err
	}
	return query, nil
}

// where builds the page's WHERE clause and its arguments: positions after the
// cursor, narrowed by the filters the caller set.
func (q readQuery) where() (string, []any) {
	clause := "position > $1"
	args := []any{q.after}
	add := func(comparison string, value any) {
		args = append(args, value)
		clause += " AND " + comparison + " $" + strconv.Itoa(len(args))
	}
	if q.action != "" {
		add("action =", q.action)
	}
	if q.actorID != "" {
		add("actor_id =", q.actorID)
	}
	if q.targetType != "" {
		add("target_type =", q.targetType)
	}
	if q.targetID != "" {
		add("target_id =", q.targetID)
	}
	if q.outcome != "" {
		add("outcome =", q.outcome)
	}
	if !q.from.IsZero() {
		add("occurred_at >=", q.from)
	}
	if !q.to.IsZero() {
		add("occurred_at <=", q.to)
	}
	return clause, args
}
