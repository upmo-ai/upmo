package audit

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/httpserver"
)

// statusRecorder keeps the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.NewResponseController reach the underlying writer to flush.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// onUnrecorded is the hook that turns the missing-event fallback of a test
// build into a test failure.
var onUnrecorded func(action Action)

// Middleware wraps the whole mux. The mux resolves the pattern when an outer
// handler refused the request before the mux ran, leaving r.Pattern empty, so
// a cross-origin refusal on a table route is still recorded as denied.
func Middleware(routes Table, mux *http.ServeMux, pool *pgxpool.Pool, service string, trusted []*net.IPNet, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := uuid.Must(uuid.NewV7()).String()
			w.Header().Set("X-Request-Id", requestID)
			s := &scope{
				service:   service,
				actor:     Actor{Type: "anonymous", CredentialType: "none"},
				requestID: requestID,
				sourceIP:  httpserver.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), trusted),
				userAgent: r.UserAgent(),
			}
			r = r.WithContext(context.WithValue(r.Context(), scopeKey{}, s))
			recorder := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r)

			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			pattern := r.Pattern
			if pattern == "" {
				_, pattern = mux.Handler(r)
			}
			route, ok := routes[pattern]
			if !ok || route.Action == "" {
				return
			}
			event, write := eventFor(route, status, s, log)
			if write {
				// ServeHTTP has returned, so the request context is already
				// canceled and the write runs on one that only carries its
				// values. The response has gone; a failed write can only be
				// logged.
				writeEvent(context.WithoutCancel(r.Context()), pool, event, log)
			}
		})
	}
}

// eventFor picks the event a finished request leaves behind: denied for a
// refusal, failure for a broken handler, and a success fallback when a
// changing route committed no event of its own. Other statuses record nothing.
func eventFor(route Route, status int, s *scope, log *slog.Logger) (Event, bool) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests:
		return annotate(Event{Action: route.Action, Outcome: Denied}, s), true
	case status >= http.StatusInternalServerError:
		return annotate(Event{Action: route.Action, Outcome: Failure}, s), true
	case status >= 200 && status < 400 && !s.recorded && !s.noChange:
		log.Error("audit event missing", "action", string(route.Action))
		if onUnrecorded != nil {
			onUnrecorded(route.Action)
		}
		return annotate(Event{Action: route.Action, Outcome: Success, Reason: "unrecorded"}, s), true
	default:
		return Event{}, false
	}
}

// annotate merges what Annotate put in the scope onto the event.
func annotate(event Event, s *scope) Event {
	if event.Reason == "" {
		event.Reason = s.annotated.Reason
	}
	if event.TargetType == "" {
		event.TargetType = s.annotated.TargetType
	}
	if event.TargetID == "" {
		event.TargetID = s.annotated.TargetID
	}
	return event
}

// writeEvent stores the event in its own short transaction: the business
// transaction either never opened, or rolled back with a failed response and
// took its pending row with it.
func writeEvent(ctx context.Context, pool *pgxpool.Pool, event Event, log *slog.Logger) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Error("audit event write failed", "err", err)
		return
	}
	if err := Record(ctx, tx, event); err != nil {
		_ = tx.Rollback(ctx)
		log.Error("audit event write failed", "err", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		log.Error("audit event write failed", "err", err)
	}
}
