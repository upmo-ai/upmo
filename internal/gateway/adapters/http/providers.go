package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// maxProviderBody bounds a provider request. A key is at most 4 KiB and a
// model list under 5 KiB, so this is generous.
const maxProviderBody = 64 << 10

// ProvidersHandler serves a signed-in person's own provider accounts and the
// ones shared with them beside them: their teams' and the workspace's. A key
// is accepted but never returned.
type ProvidersHandler struct {
	Service app.Providers
	// Access names the signed-in person. It is false without a session.
	Access func(r *http.Request) (userID string, ok bool)
	// Log records unexpected failures. It never receives a key.
	Log *slog.Logger
}

// MountProviders registers the provider routes. guard runs first.
func (h ProvidersHandler) MountProviders(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.HandlerFunc) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/providers", wrap(h.list))
	mux.Handle("GET /api/providers/sharing", wrap(h.sharing))
	mux.Handle("POST /api/providers", wrap(h.add))
	mux.Handle("POST /api/providers/test", wrap(h.test))
	mux.Handle("PUT /api/providers/{id}", wrap(h.change))
	mux.Handle("DELETE /api/providers/{id}", wrap(h.remove))
}

type providerJSON struct {
	ID       string   `json:"id"`
	Provider string   `json:"provider"`
	Name     string   `json:"name"`
	Adapter  string   `json:"adapter"`
	PlanKind string   `json:"plan_kind"`
	BaseURL  string   `json:"base_url,omitempty"`
	Status   string   `json:"status"`
	Models   []string `json:"models"`
	// Usable says whether the person can use the account now, and
	// UsableModels lists the models of theirs that count: the rule is
	// domain's, computed per account here.
	Usable       bool     `json:"usable"`
	UsableModels []string `json:"usable_models"`
	// Scope is own, workspace or team.
	Scope string `json:"scope"`
	// TeamIDs and TeamNames are the teams a team account is shared with, in
	// the same order. They are empty for any other scope.
	TeamIDs   []string `json:"team_ids"`
	TeamNames []string `json:"team_names"`
	// CanManage says whether the person may change or remove the account.
	CanManage bool `json:"can_manage"`
}

func providerBody(p domain.Provider) providerJSON {
	scope := p.Share.Scope
	if scope == "" {
		scope = domain.ScopeWorkspace
		if p.Own {
			scope = domain.ScopeOwn
		}
	}
	models := p.Models
	if models == nil {
		models = []string{}
	}
	teamIDs, teamNames := p.Share.TeamIDs, p.Share.TeamNames
	if teamIDs == nil {
		teamIDs = []string{}
	}
	if teamNames == nil {
		teamNames = []string{}
	}
	usable := domain.UsableModels(p)
	return providerJSON{
		ID:           p.ID,
		Provider:     p.Provider,
		Name:         p.Name,
		Adapter:      p.Adapter,
		PlanKind:     p.PlanKind,
		BaseURL:      p.BaseURL,
		Status:       p.Status,
		Models:       models,
		Usable:       len(usable) > 0,
		UsableModels: usable,
		Scope:        scope,
		TeamIDs:      teamIDs,
		TeamNames:    teamNames,
		CanManage:    p.Share.CanManage,
	}
}

func (h ProvidersHandler) person(w http.ResponseWriter, r *http.Request) (string, bool) {
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

// decode reads a bounded JSON body. It answers 400 or 413 itself on failure.
func decodeProvider(w http.ResponseWriter, r *http.Request, into any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProviderBody)).Decode(into)
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

func (h ProvidersHandler) list(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	rows, err := h.Service.List(r.Context(), user)
	if err != nil {
		h.refuse(w, err)
		return
	}
	out := make([]providerJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, providerBody(row))
	}
	writeRouteJSON(w, http.StatusOK, out)
}

// sharing answers whether the person may share accounts, so the screen knows
// whether to offer the choice.
func (h ProvidersHandler) sharing(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	can, err := h.Service.CanShare(r.Context(), user)
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, map[string]bool{"can_share": can})
}

func (h ProvidersHandler) add(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	if !h.Service.Keys.Ready() {
		h.refuse(w, app.ErrNoMasterKey)
		return
	}
	var body struct {
		Provider string   `json:"provider"`
		Name     string   `json:"name"`
		Adapter  string   `json:"adapter"`
		PlanKind string   `json:"plan_kind"`
		BaseURL  string   `json:"base_url"`
		APIKey   string   `json:"api_key"`
		Models   []string `json:"models"`
		Scope    string   `json:"scope"`
		TeamIDs  []string `json:"team_ids"`
	}
	if !decodeProvider(w, r, &body) {
		return
	}
	made, err := h.Service.Add(r.Context(), user, app.NewProvider{
		Provider: body.Provider,
		Name:     body.Name,
		Adapter:  body.Adapter,
		PlanKind: body.PlanKind,
		BaseURL:  body.BaseURL,
		APIKey:   body.APIKey,
		Models:   body.Models,
		Scope:    body.Scope,
		TeamIDs:  body.TeamIDs,
	})
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusCreated, providerBody(made))
}

func (h ProvidersHandler) change(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	if !h.Service.Keys.Ready() {
		h.refuse(w, app.ErrNoMasterKey)
		return
	}
	var body struct {
		Name     *string   `json:"name"`
		PlanKind *string   `json:"plan_kind"`
		BaseURL  *string   `json:"base_url"`
		APIKey   *string   `json:"api_key"`
		Models   *[]string `json:"models"`
		Scope    *string   `json:"scope"`
		TeamIDs  *[]string `json:"team_ids"`
	}
	if !decodeProvider(w, r, &body) {
		return
	}
	made, err := h.Service.Change(r.Context(), user, r.PathValue("id"), app.ProviderChange{
		Name:     body.Name,
		PlanKind: body.PlanKind,
		BaseURL:  body.BaseURL,
		APIKey:   body.APIKey,
		Models:   body.Models,
		Scope:    body.Scope,
		TeamIDs:  body.TeamIDs,
	})
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, providerBody(made))
}

func (h ProvidersHandler) remove(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	if !h.Service.Keys.Ready() {
		h.refuse(w, app.ErrNoMasterKey)
		return
	}
	if err := h.Service.Remove(r.Context(), user, r.PathValue("id")); err != nil {
		h.refuse(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h ProvidersHandler) test(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Adapter  string `json:"adapter"`
		PlanKind string `json:"plan_kind"`
		APIKey   string `json:"api_key"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
	}
	if !decodeProvider(w, r, &body) {
		return
	}
	result, err := h.Service.Test(r.Context(), user, app.TestRequest{
		ID:       body.ID,
		Provider: body.Provider,
		Adapter:  body.Adapter,
		PlanKind: body.PlanKind,
		APIKey:   body.APIKey,
		BaseURL:  body.BaseURL,
		Model:    body.Model,
	})
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, result)
}

// refuse answers a failed provider request with a fixed plain message. Nothing
// from the error text reaches the caller except a validation message, which is
// written by this package's domain and never carries input back.
func (h ProvidersHandler) refuse(w http.ResponseWriter, err error) {
	var invalid domain.Invalid
	var inUse app.InUseError
	switch {
	case errors.As(err, &invalid):
		writeRouteError(w, http.StatusUnprocessableEntity, string(invalid))
	case errors.Is(err, app.ErrNotFound):
		writeRouteError(w, http.StatusNotFound, "Not found.")
	case errors.Is(err, app.ErrGlobal):
		writeRouteError(w, http.StatusForbidden, "This provider is shared. Only workspace managers can change it.")
	case errors.Is(err, app.ErrShareOnly):
		writeRouteError(w, http.StatusForbidden, "Only workspace admins can share a provider.")
	case errors.Is(err, app.ErrDuplicate):
		writeRouteError(w, http.StatusConflict, "You already have a provider with that name.")
	case errors.Is(err, app.ErrLimit):
		writeRouteError(w, http.StatusUnprocessableEntity, fmt.Sprintf("You can add up to %d providers.", domain.MaxOwnAccounts))
	case errors.As(err, &inUse):
		text := "Used by a route"
		if len(inUse.Routes) > 0 {
			text = "Used by route " + strings.Join(inUse.Routes, ", ")
		}
		writeRouteError(w, http.StatusConflict, text)
	case errors.Is(err, app.ErrNoMasterKey):
		writeRouteError(w, http.StatusServiceUnavailable, "Saving provider keys is not set up on this server.")
	case errors.Is(err, app.ErrRate):
		writeRouteError(w, http.StatusTooManyRequests, "Too many tests. Try again in a minute.")
	case errors.Is(err, app.ErrLocalPlan):
		writeRouteError(w, http.StatusForbidden, "Only the workspace owner or an admin can use a provider on the local plan.")
	default:
		if h.Log != nil {
			h.Log.Error("providers failed", "err", err)
		}
		writeRouteError(w, http.StatusInternalServerError, "providers")
	}
}
