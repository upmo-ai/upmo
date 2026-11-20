package httpapi

import (
	"encoding/csv"
	"errors"
	"log/slog"
	"net/http"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// UsageHandler serves a signed-in person's own usage and, to a usage:view
// holder, the workspace's.
type UsageHandler struct {
	Service app.UsageService
	Access  func(r *http.Request) (userID string, ok bool)
	Log     *slog.Logger
}

// MountUsage registers the usage routes. guard runs first.
func (h UsageHandler) MountUsage(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/usage", wrap(http.HandlerFunc(h.personal)))
	mux.Handle("GET /api/usage/workspace", wrap(http.HandlerFunc(h.workspace)))
}

func (h UsageHandler) personal(w http.ResponseWriter, r *http.Request) {
	userID, ok := signedInPerson(w, r, h.Access)
	if !ok {
		return
	}
	rng, err := domain.ParseUsageRange(r.URL.Query().Get("range"))
	if err != nil {
		h.refuse(w, err)
		return
	}
	got, err := h.Service.Personal(r.Context(), userID, rng)
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, got)
}

func (h UsageHandler) workspace(w http.ResponseWriter, r *http.Request) {
	userID, ok := signedInPerson(w, r, h.Access)
	if !ok {
		return
	}
	q := r.URL.Query()
	rng, err := domain.ParseUsageRange(q.Get("range"))
	if err != nil {
		h.refuse(w, err)
		return
	}
	group, err := domain.ParseUsageGroup(q.Get("group"))
	if err != nil {
		h.refuse(w, err)
		return
	}
	format := q.Get("format")
	switch format {
	case "":
		format = "json"
	case "json", "csv":
	default:
		h.refuse(w, domain.Invalid("format must be json or csv."))
		return
	}
	got, err := h.Service.Workspace(r.Context(), userID, rng, group, format)
	if err != nil {
		h.refuse(w, err)
		return
	}
	if format == "json" {
		writeRouteJSON(w, http.StatusOK, got)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="usage-workspace-`+string(group)+"-"+string(rng)+`.csv"`)
	w.WriteHeader(http.StatusOK)
	out := csv.NewWriter(w)
	_ = out.WriteAll(domain.UsageCSV(got))
}

func (h UsageHandler) refuse(w http.ResponseWriter, err error) {
	var invalid domain.Invalid
	switch {
	case errors.As(err, &invalid):
		writeRouteError(w, http.StatusBadRequest, string(invalid))
	case errors.Is(err, app.ErrUsageForbidden):
		writeRouteError(w, http.StatusForbidden, "Only people who may view usage can see the workspace's usage.")
	default:
		if h.Log != nil {
			h.Log.Error("usage failed", "err", err)
		}
		writeRouteError(w, http.StatusInternalServerError, "usage")
	}
}
