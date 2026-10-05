package notices

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Lister returns persisted notices for one person.
type Lister interface {
	List(ctx context.Context, userID uuid.UUID) ([]Notice, error)
}

// Handler serves GET /api/notifications for a signed-in person.
type Handler struct {
	List Lister
	User func(r *http.Request) (uuid.UUID, bool)
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
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	rows, err := h.List.List(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "notifications")
		return
	}
	body := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		body = append(body, map[string]string{
			"id":    row.ID.String(),
			"kind":  row.Kind,
			"title": row.Title,
		})
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return uuid.Nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.User == nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return uuid.Nil, false
	}
	userID, ok := h.User(r)
	if !ok || userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
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
