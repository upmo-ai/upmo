package notices

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	// DefaultLimit is how many notices a list holds when it asks for no limit.
	DefaultLimit = 50
	// MaxLimit is the most a list holds, whatever it asks for.
	MaxLimit = 100
)

// Lister returns persisted notices for one person, newest first, at most limit.
type Lister interface {
	List(ctx context.Context, userID uuid.UUID, limit int) ([]Notice, error)
}

// Dismisser hides one notice of one person. It reports false when the notice
// is not theirs or does not exist.
type Dismisser interface {
	Dismiss(ctx context.Context, userID, id uuid.UUID) (bool, error)
}

// Handler serves GET /api/notifications and DELETE /api/notifications/{id}
// for a signed-in person.
type Handler struct {
	List    Lister
	Dismiss Dismisser
	User    func(r *http.Request) (uuid.UUID, bool)
}

// noticeJSON is one notification. The task id is left out when there is none.
type noticeJSON struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	TaskID string `json:"task_id,omitempty"`
}

// Mount registers notification routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/notifications", wrap(http.HandlerFunc(h.list)))
	mux.Handle("DELETE /api/notifications/{id}", wrap(http.HandlerFunc(h.dismiss)))
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	limit, ok := limitOf(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "limit must be a whole number from 1")
		return
	}
	rows, err := h.List.List(r.Context(), userID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "notifications")
		return
	}
	body := make([]noticeJSON, 0, len(rows))
	for _, row := range rows {
		item := noticeJSON{ID: row.ID.String(), Kind: row.Kind, Title: row.Title}
		if row.TaskID != nil {
			item.TaskID = row.TaskID.String()
		}
		body = append(body, item)
	}
	writeJSON(w, http.StatusOK, body)
}

// limitOf reads ?limit. No limit is DefaultLimit, a larger one is MaxLimit, and
// anything that is not a whole number from 1 is refused.
func limitOf(r *http.Request) (int, bool) {
	raw := r.URL.Query()["limit"]
	if len(raw) == 0 {
		return DefaultLimit, true
	}
	limit, err := strconv.Atoi(raw[0])
	if err != nil || limit < 1 {
		return 0, false
	}
	return min(limit, MaxLimit), true
}

func (h Handler) dismiss(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such notification")
		return
	}
	if h.Dismiss == nil {
		writeError(w, http.StatusInternalServerError, "notifications")
		return
	}
	found, err := h.Dismiss.Dismiss(r.Context(), userID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "notifications")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no such notification")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return uuid.Nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.User == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	userID, ok := h.User(r)
	if !ok || userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	return userID, true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, map[string]string{"error": text})
}
