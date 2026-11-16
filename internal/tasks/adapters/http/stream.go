package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/stream"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

type statusJSON struct {
	Status string `json:"status"`
	Phase  string `json:"phase"`
}

// resumeAfter returns the id of the last event the client has: the Last-Event-ID
// header, which wins, else the after query. It reports false for one that is
// no id.
func resumeAfter(r *http.Request) (string, bool) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return "", true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// stream sends a task's events to a signed-in person as server-sent events:
// every event after the one the client has, then each new one, until the task
// is Done or Failed.
func (h Handler) stream(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Stream == nil {
		writeError(w, http.StatusInternalServerError, "task")
		return
	}
	after, ok := resumeAfter(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "the event id is not valid")
		return
	}
	view, err := app.ViewTask(r.Context(), h.View, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	// With no cursor the client already has the task as GET showed it, so
	// the stream starts after the last event read here.
	if after == "" && len(view.Events) > 0 {
		after = view.Events[len(view.Events)-1].ID
	}
	spec := app.StreamSpec{
		TaskID: view.ID,
		Person: access.UserID,
		Access: access,
		After:  after,
	}
	if h.Renew != nil {
		spec.Renew = func(ctx context.Context) (app.Access, bool, error) { return h.Renew(ctx, r) }
	}
	sub, err := h.Stream.Subscribe(spec)
	switch {
	case errors.Is(err, app.ErrStreamPerson):
		writeError(w, http.StatusTooManyRequests, "too many open streams")
		return
	case errors.Is(err, app.ErrStreamFull):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "too many open streams")
		return
	case err != nil:
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "streams are not available")
		return
	}
	reason := "client left"
	defer func() { sub.Release(reason) }()
	h.Metrics.IncSSEStream("tasks")
	defer h.Metrics.DecSSEStream("tasks")

	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-store")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := stream.NewSSEWriter(w)

	if out.Hello() != nil ||
		writeStatus(out, view.Status, view.Phase) != nil {
		reason = "write failed"
		return
	}

	beat := time.NewTicker(h.Stream.Heartbeat())
	defer beat.Stop()
	life := time.NewTimer(h.Stream.Lifetime())
	defer life.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-life.C:
			reason = "lifetime"
			return
		case <-beat.C:
			err = out.Heartbeat()
		case <-sub.Closed():
			reason = sub.Reason()
			if reason == app.EndAccess {
				_ = out.End(app.EndAccess)
			}
			return
		case m := <-sub.Out():
			err = writeMessage(out, m)
			sub.Taken()
			if err == nil && m.Kind == app.StreamEnd {
				reason = app.EndFinished
				return
			}
		}
		if err != nil {
			reason = "write failed"
			return
		}
	}
}

func writeStatus(s stream.SSEWriter, status domain.Shown, phase domain.Status) error {
	return s.Event("status", "", statusJSON{Status: string(status), Phase: string(phase)})
}

func writeMessage(s stream.SSEWriter, m app.StreamMessage) error {
	switch m.Kind {
	case app.StreamLine:
		return s.Event("line", m.Event.ID, eventBody(m.Event))
	case app.StreamStatus:
		return writeStatus(s, m.Summary.Status, m.Summary.Phase)
	case app.StreamEnd:
		return s.End(app.EndFinished)
	default:
		return nil
	}
}
