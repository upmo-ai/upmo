// Package httpapi serves chat open, list, say and read for a signed-in person.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// Handler opens, lists, appends and reads chats through the existing book.
// Stream, when set, serves the chat stream; Renew then looks a person's access
// up again for the streams that stay open.
type Handler struct {
	Book   app.Book
	Stream *app.StreamHub
	Access func(r *http.Request) (app.Access, bool)
	Renew  func(ctx context.Context, r *http.Request) (app.Access, bool, error)
}

// Mount registers the chat routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/chats", wrap(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/chats", wrap(http.HandlerFunc(h.open)))
	mux.Handle("POST /api/chats/{id}", wrap(http.HandlerFunc(h.say)))
	mux.Handle("GET /api/chats/{id}", wrap(http.HandlerFunc(h.read)))
	mux.Handle("POST /api/chats/{id}/lines/{line}/retry", wrap(http.HandlerFunc(h.retry)))
	mux.Handle("GET /api/chats/{id}/stream", wrap(http.HandlerFunc(h.stream)))
}

func (h Handler) open(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	summary, err := app.Open(r.Context(), h.Book, access)
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, summaryBody(summary))
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	summaries, err := app.List(r.Context(), h.Book, access)
	if refuse(w, err) {
		return
	}
	body := make([]summaryJSON, 0, len(summaries))
	for _, summary := range summaries {
		body = append(body, summaryBody(summary))
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) say(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		Body  *string `json:"body"`
		Route string  `json:"route"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Body == nil {
		writeError(w, http.StatusBadRequest, "a chat needs a line")
		return
	}
	if err := app.Say(r.Context(), h.Book, access, r.PathValue("id"), *input.Body, input.Route); refuse(w, err) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h Handler) retry(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		Route string `json:"route"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "the retry needs a route or nothing")
		return
	}
	if err := app.Retry(r.Context(), h.Book, access, r.PathValue("id"), r.PathValue("line"), input.Route); refuse(w, err) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h Handler) read(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	got, err := app.Read(r.Context(), h.Book, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, chatBody(got))
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (app.Access, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return app.Access{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return app.Access{}, false
	}
	access, ok := h.Access(r)
	if !ok || access.UserID == "" || access.Allow == "" {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return app.Access{}, false
	}
	return access, true
}

func refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, app.ErrAccess):
		writeError(w, http.StatusForbidden, "unauthorised")
	case errors.Is(err, app.ErrGone):
		writeError(w, http.StatusNotFound, "no such chat")
	case errors.Is(err, app.ErrSay):
		writeError(w, http.StatusForbidden, "needs a chat you can edit")
	case errors.Is(err, app.ErrNoLine):
		writeError(w, http.StatusNotFound, "no such line")
	case errors.Is(err, app.ErrRetry):
		writeError(w, http.StatusConflict, "that line cannot be retried")
	case errors.Is(err, app.ErrBusy):
		writeError(w, http.StatusConflict, "a reply is already on its way")
	case errors.Is(err, app.ErrReplies):
		writeError(w, http.StatusServiceUnavailable, "replies are off")
	case errors.Is(err, app.ErrChat), errors.Is(err, domain.ErrLine), errors.Is(err, domain.ErrRoute):
		writeError(w, http.StatusBadRequest, "the chat input was refused")
	case errors.Is(err, domain.ErrWriter):
		writeError(w, http.StatusConflict, "the chat cannot take that change")
	default:
		writeError(w, http.StatusInternalServerError, "chat")
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, map[string]string{"error": text})
}
