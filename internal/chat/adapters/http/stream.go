package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/stream"
)

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

type (
	deltaJSON struct {
		LineID string `json:"line_id"`
		Seq    int    `json:"seq"`
		Body   string `json:"body"`
	}
	noticeJSON struct {
		LineID      string `json:"line_id"`
		FailedModel string `json:"failed_model"`
		Reason      string `json:"reason"`
		NextModel   string `json:"next_model"`
	}
)

// stream sends a chat's rows to a signed-in person as server-sent events:
// every delta and line after the one the client has, then each new one. It
// has no end except the loss of access.
func (h Handler) stream(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Stream == nil {
		writeError(w, http.StatusInternalServerError, "chat")
		return
	}
	after, ok := resumeAfter(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "the event id is not valid")
		return
	}
	chatID := r.PathValue("id")
	if _, err := uuid.Parse(chatID); err != nil {
		writeError(w, http.StatusBadRequest, "the chat input was refused")
		return
	}
	spec := app.StreamSpec{ChatID: chatID, Person: access.UserID, Access: access, After: after}
	if h.Renew != nil {
		spec.Renew = func(ctx context.Context) (app.Access, bool, error) { return h.Renew(ctx, r) }
	}
	sub, err := h.Stream.Subscribe(r.Context(), spec)
	switch {
	case errors.Is(err, app.ErrGone):
		writeError(w, http.StatusNotFound, "no such chat")
		return
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
	h.Metrics.IncSSEStream("chat")
	defer h.Metrics.DecSSEStream("chat")

	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-store")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := stream.NewSSEWriter(w)

	if out.Hello() != nil {
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
			err = writeEvent(out, m.Event)
			sub.Taken()
		}
		if err != nil {
			reason = "write failed"
			return
		}
	}
}

// writeEvent writes one delta, status or line. Its id is the row's, so a
// client that reconnects resumes after it.
func writeEvent(s stream.SSEWriter, e app.StreamEvent) error {
	switch e.Kind {
	case app.EventDelta:
		return s.Event("delta", e.ID, deltaJSON{LineID: e.LineID, Seq: e.Seq, Body: e.Body})
	case app.EventStatus:
		return s.Event("status", e.ID, noticeJSON{
			LineID:      e.LineID,
			FailedModel: e.Notice.FailedModel,
			Reason:      domain.Reason(e.Notice.Class),
			NextModel:   e.Notice.NextModel,
		})
	case app.EventLine:
		return s.Event("line", e.ID, lineBody(e.Line))
	default:
		return nil
	}
}
