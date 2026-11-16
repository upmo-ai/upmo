package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

const maxRouteBody = 64 << 10

// AccountList reads the provider accounts one person can use: their own and
// the workspace's.
type AccountList interface {
	List(ctx context.Context, user string) ([]domain.Provider, error)
}

// RoutesHandler serves gateway route listing and CRUD for signed-in users.
// The rows hold route names and model chains, never a credential.
type RoutesHandler struct {
	Book    app.RouteBook
	Service app.RouteService
	Access  func(r *http.Request) (userID string, ok bool)
	// Accounts reads what the calling person can use, so each route is
	// marked usable or not. Nil marks none usable; a read that fails refuses
	// list requests.
	Accounts AccountList
	Log      *slog.Logger
}

// MountRoutes registers the gateway route endpoints. guard runs first.
func (h RoutesHandler) MountRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/gateway/routes", wrap(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/gateway/routes", wrap(http.HandlerFunc(h.create)))
	mux.Handle("PATCH /api/gateway/routes/{id}", wrap(http.HandlerFunc(h.update)))
	mux.Handle("DELETE /api/gateway/routes/{id}", wrap(http.HandlerFunc(h.remove)))
	mux.Handle("PUT /api/gateway/routes/{id}/share", wrap(http.HandlerFunc(h.share)))
}

type routeHopBody struct {
	Position             *int     `json:"position"`
	AccountID            string   `json:"account_id"`
	Model                string   `json:"model"`
	Weight               *int     `json:"weight"`
	Retry                *int     `json:"retry"`
	RequiredCapabilities []string `json:"required_capabilities"`
}

func toNewRouteHops(in []routeHopBody) []app.NewRouteHop {
	if in == nil {
		return nil
	}
	out := make([]app.NewRouteHop, 0, len(in))
	for _, h := range in {
		out = append(out, app.NewRouteHop{
			Position:             h.Position,
			AccountID:            h.AccountID,
			Model:                h.Model,
			Weight:               h.Weight,
			Retry:                h.Retry,
			RequiredCapabilities: h.RequiredCapabilities,
		})
	}
	return out
}

func (h RoutesHandler) person(w http.ResponseWriter, r *http.Request) (string, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeRouteError(w, http.StatusForbidden, "needs a signed-in person")
		return "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeRouteError(w, http.StatusUnauthorized, "unauthorised")
		return "", false
	}
	userID, ok := h.Access(r)
	if !ok || userID == "" {
		writeRouteError(w, http.StatusUnauthorized, "unauthorised")
		return "", false
	}
	return userID, true
}

func decodeRoute(w http.ResponseWriter, r *http.Request, into any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRouteBody)).Decode(into)
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeRouteError(w, http.StatusRequestEntityTooLarge, "That request is too large.")
		return false
	}
	writeRouteError(w, http.StatusBadRequest, "bad request")
	return false
}

func (h RoutesHandler) markRouteUsable(ctx context.Context, userID string, rows []app.ListedRoute, checkCanUse bool) error {
	if h.Accounts != nil {
		accounts, err := h.Accounts.List(ctx, userID)
		if err != nil {
			return err
		}
		app.MarkUsable(rows, accounts)
	}
	if checkCanUse {
		for i := range rows {
			if !rows[i].CanUse {
				rows[i].Usable = false
			}
		}
	}
	return nil
}

func (h RoutesHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	var (
		rows        []app.ListedRoute
		err         error
		checkCanUse bool
	)
	if h.Service.Store != nil {
		rows, err = h.Service.List(r.Context(), userID)
		checkCanUse = true
	} else {
		rows, err = app.ListRoutes(r.Context(), h.Book)
	}
	if err != nil {
		h.refuse(w, err)
		return
	}
	if err := h.markRouteUsable(r.Context(), userID, rows, checkCanUse); err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, rows)
}

func (h RoutesHandler) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Service.Store == nil {
		writeRouteError(w, http.StatusInternalServerError, "gateway")
		return
	}
	var body struct {
		Name       string         `json:"name"`
		Capability string         `json:"capability"`
		Decision   string         `json:"decision"`
		Visibility string         `json:"visibility"`
		TeamIDs    []string       `json:"team_ids"`
		Hops       []routeHopBody `json:"hops"`
	}
	if !decodeRoute(w, r, &body) {
		return
	}
	made, err := h.Service.Create(r.Context(), userID, app.NewRoute{
		Name:       body.Name,
		Capability: body.Capability,
		Decision:   body.Decision,
		Visibility: body.Visibility,
		TeamIDs:    body.TeamIDs,
		Hops:       toNewRouteHops(body.Hops),
	})
	if err != nil {
		h.refuse(w, err)
		return
	}
	single := []app.ListedRoute{made}
	_ = h.markRouteUsable(r.Context(), userID, single, true)
	writeRouteJSON(w, http.StatusCreated, single[0])
}

func (h RoutesHandler) update(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Service.Store == nil {
		writeRouteError(w, http.StatusInternalServerError, "gateway")
		return
	}
	var body struct {
		Name       *string         `json:"name"`
		Capability *string         `json:"capability"`
		Decision   *string         `json:"decision"`
		Visibility *string         `json:"visibility"`
		TeamIDs    *[]string       `json:"team_ids"`
		Hops       *[]routeHopBody `json:"hops"`
	}
	if !decodeRoute(w, r, &body) {
		return
	}
	var hops *[]app.NewRouteHop
	if body.Hops != nil {
		converted := toNewRouteHops(*body.Hops)
		hops = &converted
	}
	made, err := h.Service.Update(r.Context(), userID, r.PathValue("id"), app.RouteChange{
		Name:       body.Name,
		Capability: body.Capability,
		Decision:   body.Decision,
		Visibility: body.Visibility,
		TeamIDs:    body.TeamIDs,
		Hops:       hops,
	})
	if err != nil {
		h.refuse(w, err)
		return
	}
	single := []app.ListedRoute{made}
	_ = h.markRouteUsable(r.Context(), userID, single, true)
	writeRouteJSON(w, http.StatusOK, single[0])
}

func (h RoutesHandler) share(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Service.Store == nil {
		writeRouteError(w, http.StatusInternalServerError, "gateway")
		return
	}
	var body struct {
		Visibility string   `json:"visibility"`
		TeamIDs    []string `json:"team_ids"`
	}
	if !decodeRoute(w, r, &body) {
		return
	}
	made, err := h.Service.Share(r.Context(), userID, r.PathValue("id"), body.Visibility, body.TeamIDs)
	if err != nil {
		h.refuse(w, err)
		return
	}
	single := []app.ListedRoute{made}
	_ = h.markRouteUsable(r.Context(), userID, single, true)
	writeRouteJSON(w, http.StatusOK, single[0])
}

func (h RoutesHandler) remove(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Service.Store == nil {
		writeRouteError(w, http.StatusInternalServerError, "gateway")
		return
	}
	if err := h.Service.Remove(r.Context(), userID, r.PathValue("id")); err != nil {
		h.refuse(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h RoutesHandler) refuse(w http.ResponseWriter, err error) {
	var invalid domain.Invalid
	switch {
	case errors.As(err, &invalid):
		writeRouteError(w, http.StatusUnprocessableEntity, string(invalid))
	case errors.Is(err, app.ErrNotFound):
		writeRouteError(w, http.StatusNotFound, "Not found.")
	case errors.Is(err, app.ErrRouteForbidden), errors.Is(err, app.ErrGlobal):
		writeRouteError(w, http.StatusForbidden, "This route cannot be changed.")
	case errors.Is(err, app.ErrRouteShareOnly), errors.Is(err, app.ErrShareOnly):
		writeRouteError(w, http.StatusForbidden, "Only workspace admins can share a route.")
	case errors.Is(err, app.ErrRouteAccountForbidden):
		writeRouteError(w, http.StatusForbidden, "You cannot use that provider account.")
	case errors.Is(err, app.ErrRouteDuplicate), errors.Is(err, app.ErrDuplicate):
		writeRouteError(w, http.StatusConflict, "A route with that name already exists.")
	default:
		if h.Log != nil {
			h.Log.Error("routes failed", "err", err)
		}
		writeRouteError(w, http.StatusInternalServerError, "gateway")
	}
}

func writeRouteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeRouteError(w http.ResponseWriter, status int, text string) {
	writeRouteJSON(w, status, map[string]string{"error": text})
}
