package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/app"
)

// RoutesHandler lists gateway routes for a signed-in settings manager.
type RoutesHandler struct {
	Book   app.RouteBook
	Access func(r *http.Request) (userID, allow string, ok bool)
}

// MountRoutes registers GET /api/gateway/routes. guard runs first.
func (h RoutesHandler) MountRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/gateway/routes", wrap(http.HandlerFunc(h.list)))
}

func (h RoutesHandler) list(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeRouteError(w, http.StatusForbidden, "needs a signed-in person")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeRouteError(w, http.StatusUnauthorized, "unauthorised")
		return
	}
	userID, allow, ok := h.Access(r)
	if !ok || userID == "" || allow == "" || !allows(allow, "settings:manage") {
		writeRouteError(w, http.StatusUnauthorized, "unauthorised")
		return
	}
	rows, err := app.ListRoutes(r.Context(), h.Book)
	if err != nil {
		writeRouteError(w, http.StatusInternalServerError, "gateway")
		return
	}
	writeRouteJSON(w, http.StatusOK, rows)
}

func allows(list, name string) bool {
	for _, part := range strings.Split(list, ",") {
		if strings.TrimSpace(part) == name {
			return true
		}
	}
	return false
}

func writeRouteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeRouteError(w http.ResponseWriter, status int, text string) {
	writeRouteJSON(w, status, map[string]string{"error": text})
}
