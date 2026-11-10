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

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// writeDeadline is how long each write to a stream may take. The server's
// WriteTimeout counts from the request header and would cut a long stream off,
// so every write sets a new deadline.
const writeDeadline = 30 * time.Second

// retryMillis is the reconnect delay the browser is told to use.
const retryMillis = 3000

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

	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-store")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := sseWriter{w: w, rc: http.NewResponseController(w)}

	if out.write(": ok\n\nretry: "+strconv.Itoa(retryMillis)+"\n\n") != nil {
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
			err = out.event(m.Event)
			sub.Taken()
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

func (s sseWriter) end(reason string) error {
	data, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return err
	}
	return s.write("event: end\ndata: " + string(data) + "\n\n")
}

// event writes one delta, status or line. Its id is the row's, so a client
// that reconnects resumes after it.
func (s sseWriter) event(e app.StreamEvent) error {
	var name string
	var body any
	switch e.Kind {
	case app.EventDelta:
		name, body = "delta", deltaJSON{LineID: e.LineID, Seq: e.Seq, Body: e.Body}
	case app.EventStatus:
		name, body = "status", noticeJSON{
			LineID:      e.LineID,
			FailedModel: e.Notice.FailedModel,
			Reason:      domain.Reason(e.Notice.Class),
			NextModel:   e.Notice.NextModel,
		}
	case app.EventLine:
		name, body = "line", lineBody(e.Line)
	default:
		return nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return s.write("event: " + name + "\nid: " + e.ID + "\ndata: " + string(data) + "\n\n")
}
