// Package httpapi serves task transitions for a signed-in person.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Handler applies one task transition through the existing writer.
type Handler struct {
	Book   app.Book
	Access func(r *http.Request) (app.Access, bool)
}

// Mount registers the task routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("POST /api/tasks", wrap(http.HandlerFunc(h.open)))
	mux.Handle("POST /api/tasks/{id}", wrap(http.HandlerFunc(h.step)))
}

func (h Handler) open(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		Goal *string `json:"goal"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Goal == nil {
		writeError(w, http.StatusBadRequest, "a task needs a goal")
		return
	}
	task, err := app.ApplyTransition(r.Context(), h.Book, access, "", domain.Input{
		Kind: domain.Open,
		Goal: *input.Goal,
	})
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, taskJSON(task))
}

func (h Handler) step(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
		Turn string `json:"turn"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, "a task needs a kind")
		return
	}
	kind := domain.Kind(strings.TrimSpace(input.Kind))
	switch kind {
	case domain.Advance, domain.Plan, domain.Fail, domain.Chat, domain.Pass:
	default:
		writeError(w, http.StatusBadRequest, "a task needs a kind")
		return
	}
	task, err := app.ApplyTransition(r.Context(), h.Book, access, r.PathValue("id"), domain.Input{
		Kind: kind,
		Body: input.Body,
		Turn: input.Turn,
	})
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, taskJSON(task))
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

func taskJSON(task app.Task) map[string]string {
	return map[string]string{
		"id":     task.ID,
		"status": string(task.Status),
		"goal":   task.Goal,
	}
}

func refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, app.ErrAccess):
		writeError(w, http.StatusForbidden, "unauthorised")
	case errors.Is(err, app.ErrGone):
		writeError(w, http.StatusNotFound, "no such task")
	case errors.Is(err, app.ErrTask), errors.Is(err, domain.ErrInput), errors.Is(err, domain.ErrGoal), errors.Is(err, domain.ErrChat):
		writeError(w, http.StatusBadRequest, "the task input was refused")
	case errors.Is(err, domain.ErrTerminal), errors.Is(err, domain.ErrEvidence), errors.Is(err, domain.ErrTurn), errors.Is(err, domain.ErrExhausted), errors.Is(err, domain.ErrStatus):
		writeError(w, http.StatusConflict, "the task cannot take that step")
	default:
		writeError(w, http.StatusInternalServerError, "task")
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
