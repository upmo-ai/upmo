package decisions

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Handler lists accepted decision gates.
type Handler struct {
	List   Lister
	Access func(r *http.Request) (Access, bool)
}

// Mount registers the decision routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/decisions/gates", wrap(http.HandlerFunc(h.list)))
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	rows, err := ListGates(r.Context(), h.List, access)
	if refuse(w, err) {
		return
	}
	body := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		body = append(body, map[string]any{
			"route":       row.Route,
			"kind":        row.Kind,
			"fingerprint": row.Fingerprint,
			"verdict":     row.Verdict,
			"hit":         row.Hit,
			"total":       row.Total,
			"bar":         row.Bar,
			"updated_at":  row.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (Access, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return Access{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return Access{}, false
	}
	access, ok := h.Access(r)
	if !ok || access.UserID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return Access{}, false
	}
	if access.Allow == "" {
		writeError(w, http.StatusForbidden, "unauthorised")
		return Access{}, false
	}
	return access, true
}

func refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrAccess):
		writeError(w, http.StatusForbidden, "unauthorised")
	default:
		writeError(w, http.StatusInternalServerError, "decisions")
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
