package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// writeDeadline is how long each write to a stream may take. The server's
// WriteTimeout counts from the request header and would cut a long stream off,
// so every write sets a new deadline.
const writeDeadline = 30 * time.Second

// retryMillis is the reconnect delay the browser is told to use.
const retryMillis = 3000

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

	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-store")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := sseWriter{w: w, rc: http.NewResponseController(w)}

	if out.write(": ok\n\nretry: "+strconv.Itoa(retryMillis)+"\n\n") != nil ||
		out.status(view.Status, view.Phase) != nil {
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
			err = out.write(": hb\n\n")
		case <-sub.Closed():
			reason = sub.Reason()
			if reason == app.EndAccess {
				_ = out.end(app.EndAccess)
			}
			return
		case m := <-sub.Out():
			err = out.message(m)
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

// sseWriter writes event stream frames. Each write sets a new write deadline
// and is flushed.
type sseWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (s sseWriter) write(frame string) error {
	if err := s.rc.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := io.WriteString(s.w, frame); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s sseWriter) status(status domain.Shown, phase domain.Status) error {
	data, err := json.Marshal(statusJSON{Status: string(status), Phase: string(phase)})
	if err != nil {
		return err
	}
	return s.write("event: status\ndata: " + string(data) + "\n\n")
}

func (s sseWriter) end(reason string) error {
	data, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return err
	}
	return s.write("event: end\ndata: " + string(data) + "\n\n")
}

func (s sseWriter) message(m app.StreamMessage) error {
	switch m.Kind {
	case app.StreamLine:
		data, err := json.Marshal(eventBody(m.Event))
		if err != nil {
			return err
		}
		return s.write("event: line\nid: " + m.Event.ID + "\ndata: " + string(data) + "\n\n")
	case app.StreamStatus:
		return s.status(m.Status, m.Phase)
	case app.StreamEnd:
		return s.end(app.EndFinished)
	default:
		return nil
	}
}
