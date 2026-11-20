package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// maxProviderBody bounds a provider request. A key is at most 4 KiB and a
// 100-model list is up to ~12 KiB, so 64 KiB is generous.
const maxProviderBody = 64 << 10

// maxPricingMapBody allows a full map request while the service enforces its smaller stored-map limit.
const maxPricingMapBody = 128 << 10

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
	mux.Handle("GET /api/providers/vendors", wrap(h.vendors))
	mux.Handle("GET /api/providers/catalog", wrap(h.catalog))
	mux.Handle("GET /api/providers/sharing", wrap(h.sharing))
	mux.Handle("GET /api/providers/{id}/models", wrap(h.models))
	mux.Handle("GET /api/providers/{id}/pricing", wrap(h.pricing))
	mux.Handle("GET /api/providers/{id}/uses", wrap(h.uses))
	mux.Handle("POST /api/providers", wrap(h.add))
	mux.Handle("POST /api/providers/test", wrap(h.test))
	mux.Handle("PUT /api/providers/{id}", wrap(h.change))
	mux.Handle("DELETE /api/providers/{id}", wrap(h.remove))
	mux.Handle("PUT /api/providers/{id}/overrides", wrap(h.setOverride))
	mux.Handle("PUT /api/providers/{id}/pricing-map", wrap(h.setPricingMap))
	mux.Handle("DELETE /api/providers/{id}/overrides", wrap(h.deleteOverride))
}

type providerJSON struct {
	ID              string                `json:"id"`
	Provider        string                `json:"provider"`
	Name            string                `json:"name"`
	Adapter         string                `json:"adapter"`
	PlanKind        string                `json:"plan_kind"`
	BaseURL         string                `json:"base_url,omitempty"`
	PricingProvider string                `json:"pricing_provider"`
	PricingModels   map[string]string     `json:"pricing_models"`
	Status          string                `json:"status"`
	Enabled         bool                  `json:"enabled"`
	KeyHint         string                `json:"key_hint,omitempty"`
	KeyChangedAt    *time.Time            `json:"key_changed_at,omitempty"`
	KeyChangedBy    string                `json:"key_changed_by,omitempty"`
	Models          []string              `json:"models"`
	EnabledModels   []domain.EnabledModel `json:"enabled_models"`
	// Usable says whether the person can use the account now, and
	// UsableModels lists the models of theirs that count: the rule is
	// domain's, computed per account here.
	Usable       bool     `json:"usable"`
	UsableModels []string `json:"usable_models"`
	// Visibility is private, team or everyone.
	Visibility string `json:"visibility"`
	// TeamIDs and TeamNames are the teams a team account is shared with, in
	// the same order. They are empty for any other visibility.
	TeamIDs   []string `json:"team_ids"`
	TeamNames []string `json:"team_names"`
	// CanManage says whether the person may change or remove the account.
	CanManage bool `json:"can_manage"`
	// CanUse says whether the person may route a call through the account.
	CanUse bool `json:"can_use"`
	// IsOwner says the account is this person's and not a house account.
	// It is a display flag. Access and tiering follow Visibility and House.
	IsOwner bool `json:"is_owner"`
}

func providerBody(p domain.Provider) providerJSON {
	visibility := p.Share.Visibility
	if visibility == "" {
		visibility = domain.VisibilityEveryone
		if p.Own {
			visibility = domain.VisibilityPrivate
		}
	}
	models := p.Models
	if models == nil {
		models = []string{}
	}
	enabled := p.EnabledModels
	if enabled == nil {
		enabled = make([]domain.EnabledModel, 0, len(models))
		for _, m := range models {
			enabled = append(enabled, domain.EnabledModel{ID: m, Listed: true})
		}
	}
	teamIDs, teamNames := p.Share.TeamIDs, p.Share.TeamNames
	if teamIDs == nil {
		teamIDs = []string{}
	}
	if teamNames == nil {
		teamNames = []string{}
	}
	usable := []string{}
	if p.Share.CanUse {
		usable = domain.UsableModels(p)
	}
	return providerJSON{
		ID:              p.ID,
		Provider:        p.Provider,
		Name:            p.Name,
		Adapter:         p.Adapter,
		PlanKind:        p.PlanKind,
		BaseURL:         p.BaseURL,
		PricingProvider: p.PricingProvider,
		PricingModels:   p.PricingModels,
		Status:          p.Status,
		Enabled:         p.Enabled,
		KeyHint:         p.KeyHint,
		KeyChangedAt:    p.KeyChangedAt,
		KeyChangedBy:    p.KeyChangedBy,
		Models:          models,
		EnabledModels:   enabled,
		Usable:          len(usable) > 0,
		UsableModels:    usable,
		Visibility:      visibility,
		TeamIDs:         teamIDs,
		TeamNames:       teamNames,
		CanManage:       p.Share.CanManage,
		CanUse:          p.Share.CanUse,
		IsOwner:         p.Share.IsOwner,
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
	return decodeProviderLimit(w, r, into, maxProviderBody)
}

func decodeProviderLimit(w http.ResponseWriter, r *http.Request, into any, limit int64) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(into)
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

type vendorJSON struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	Adapter        string `json:"adapter"`
	DefaultBaseURL string `json:"default_base_url"`
	SuggestedPlan  string `json:"suggested_plan,omitempty"`
}

type catalogRowJSON struct {
	Vendor         string   `json:"vendor"`
	Model          string   `json:"model"`
	Version        string   `json:"version"`
	Kind           string   `json:"kind"`
	Inputs         []string `json:"inputs"`
	Outputs        []string `json:"outputs"`
	Tools          bool     `json:"tools"`
	Structured     bool     `json:"structured"`
	Reasoning      bool     `json:"reasoning"`
	ContextWindow  int      `json:"context_window"`
	MaxOutput      *int     `json:"max_output,omitempty"`
	Lifecycle      string   `json:"lifecycle,omitempty"`
	RetiresAt      string   `json:"retires_at,omitempty"`
	InputPerMTok   *float64 `json:"input_usd_per_mtok"`
	OutputPerMTok  *float64 `json:"output_usd_per_mtok"`
	CacheReadMTok  *float64 `json:"cache_read_usd_per_mtok"`
	CacheWriteMTok *float64 `json:"cache_write_usd_per_mtok"`
}

// vendors returns the picker vendor table for a signed-in person.
func (h ProvidersHandler) vendors(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.person(w, r); !ok {
		return
	}
	list := domain.Vendors()
	out := make([]vendorJSON, 0, len(list))
	for _, v := range list {
		out = append(out, vendorJSON{
			Key:            v.Key,
			Name:           v.Name,
			Adapter:        v.Adapter,
			DefaultBaseURL: v.BaseURL,
			SuggestedPlan:  v.SuggestedPlan,
		})
	}
	writeRouteJSON(w, http.StatusOK, out)
}

// catalog returns the stored catalogue rows for the requested vendor.
func (h ProvidersHandler) catalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.person(w, r); !ok {
		return
	}
	rows, err := h.Service.VendorCatalog(r.Context(), r.URL.Query().Get("vendor"))
	if err != nil {
		h.refuse(w, err)
		return
	}
	out := make([]catalogRowJSON, 0, len(rows))
	for _, row := range rows {
		inputs := row.Inputs
		if inputs == nil {
			inputs = []string{}
		}
		outputs := row.Outputs
		if outputs == nil {
			outputs = []string{}
		}
		var maxOut *int
		if row.MaxOutput > 0 {
			v := row.MaxOutput
			maxOut = &v
		}
		var retires string
		if row.RetiresAt != nil {
			retires = row.RetiresAt.UTC().Format("2006-01-02")
		}
		out = append(out, catalogRowJSON{
			Vendor:         row.Vendor,
			Model:          row.Model,
			Version:        row.Version,
			Kind:           string(row.Kind),
			Inputs:         inputs,
			Outputs:        outputs,
			Tools:          slices.Contains(row.Caps, domain.CapTools),
			Structured:     slices.Contains(row.Caps, domain.CapStructured),
			Reasoning:      row.Reasoning,
			ContextWindow:  row.ContextWindow,
			MaxOutput:      maxOut,
			Lifecycle:      row.Lifecycle,
			RetiresAt:      retires,
			InputPerMTok:   row.Price.InputPerMTok,
			OutputPerMTok:  row.Price.OutputPerMTok,
			CacheReadMTok:  row.Price.CacheReadMTok,
			CacheWriteMTok: row.Price.CacheWriteMTok,
		})
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

// models returns the discovered model list stored for an account the person
// may see.
func (h ProvidersHandler) models(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	rows, err := h.Service.SeenModels(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.refuse(w, err)
		return
	}
	if rows == nil {
		rows = []domain.SeenModel{}
	}
	writeRouteJSON(w, http.StatusOK, rows)
}

func (h ProvidersHandler) uses(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	uses, err := h.Service.Uses(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, uses)
}

func (h ProvidersHandler) pricing(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	view, err := h.Service.GetModelPricing(r.Context(), user, r.PathValue("id"), r.URL.Query().Get("model"))
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, view)
}

func (h ProvidersHandler) setOverride(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Model                     string `json:"model"`
		InputMicroUSDPerMTok      *int64 `json:"input_micro_usd_per_mtok"`
		OutputMicroUSDPerMTok     *int64 `json:"output_micro_usd_per_mtok"`
		CacheReadMicroUSDPerMTok  *int64 `json:"cache_read_micro_usd_per_mtok"`
		CacheWriteMicroUSDPerMTok *int64 `json:"cache_write_micro_usd_per_mtok"`
	}
	if !decodeProvider(w, r, &body) {
		return
	}
	view, err := h.Service.SetPriceOverride(r.Context(), user, r.PathValue("id"), domain.PriceOverride{
		Model:                     body.Model,
		InputMicroUSDPerMTok:      body.InputMicroUSDPerMTok,
		OutputMicroUSDPerMTok:     body.OutputMicroUSDPerMTok,
		CacheReadMicroUSDPerMTok:  body.CacheReadMicroUSDPerMTok,
		CacheWriteMicroUSDPerMTok: body.CacheWriteMicroUSDPerMTok,
	})
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, view)
}

func (h ProvidersHandler) setPricingMap(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		PricingProvider *string            `json:"pricing_provider"`
		Models          map[string]*string `json:"models"`
	}
	var raw map[string]json.RawMessage
	if !decodeProviderLimit(w, r, &raw, maxPricingMapBody) {
		return
	}
	if value, ok := raw["pricing_provider"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &body.PricingProvider); err != nil {
			h.refuse(w, domain.Invalid("Invalid pricing provider."))
			return
		}
	}
	if value, ok := raw["models"]; ok {
		if err := json.Unmarshal(value, &body.Models); err != nil {
			h.refuse(w, domain.Invalid("Invalid model mappings."))
			return
		}
	}
	models := make(map[string]string, len(body.Models))
	for model, mapped := range body.Models {
		if mapped != nil {
			models[model] = *mapped
		} else {
			models[model] = ""
		}
	}
	var vendor *string
	if value, ok := raw["pricing_provider"]; ok {
		v := ""
		if string(value) != "null" && body.PricingProvider != nil {
			v = *body.PricingProvider
		}
		vendor = &v
	}
	if err := h.Service.SetPricingMap(r.Context(), user, r.PathValue("id"), vendor, models); err != nil {
		h.refuse(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h ProvidersHandler) deleteOverride(w http.ResponseWriter, r *http.Request) {
	user, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if !decodeProvider(w, r, &body) {
		return
	}
	view, err := h.Service.DeletePriceOverride(r.Context(), user, r.PathValue("id"), body.Model)
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, view)
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
		Provider   string   `json:"provider"`
		Name       string   `json:"name"`
		Adapter    string   `json:"adapter"`
		PlanKind   string   `json:"plan_kind"`
		BaseURL    string   `json:"base_url"`
		APIKey     string   `json:"api_key"`
		Models     []string `json:"models"`
		Visibility string   `json:"visibility"`
		TeamIDs    []string `json:"team_ids"`
	}
	if !decodeProvider(w, r, &body) {
		return
	}
	made, err := h.Service.Add(r.Context(), user, app.NewProvider{
		Provider:   body.Provider,
		Name:       body.Name,
		Adapter:    body.Adapter,
		PlanKind:   body.PlanKind,
		BaseURL:    body.BaseURL,
		APIKey:     body.APIKey,
		Models:     body.Models,
		Visibility: body.Visibility,
		TeamIDs:    body.TeamIDs,
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
		Name       *string   `json:"name"`
		PlanKind   *string   `json:"plan_kind"`
		BaseURL    *string   `json:"base_url"`
		APIKey     *string   `json:"api_key"`
		Models     *[]string `json:"models"`
		Visibility *string   `json:"visibility"`
		TeamIDs    *[]string `json:"team_ids"`
		Enabled    *bool     `json:"enabled"`
	}
	if !decodeProvider(w, r, &body) {
		return
	}
	made, err := h.Service.Change(r.Context(), user, r.PathValue("id"), app.ProviderChange{
		Name:       body.Name,
		PlanKind:   body.PlanKind,
		BaseURL:    body.BaseURL,
		APIKey:     body.APIKey,
		Models:     body.Models,
		Visibility: body.Visibility,
		TeamIDs:    body.TeamIDs,
		Enabled:    body.Enabled,
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
	case errors.Is(err, app.ErrOverridePermission):
		writeRouteError(w, http.StatusForbidden, "Only workspace admins can manage price overrides.")
	case errors.Is(err, app.ErrPricingMapLimit):
		writeRouteError(w, http.StatusBadRequest, "The pricing map may contain at most 2,000 models and 65,536 bytes.")
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
	case errors.Is(err, app.ErrMemberProvidersDisabled):
		writeRouteError(w, http.StatusForbidden, "Your workspace has turned off member providers.")
	default:
		if h.Log != nil {
			h.Log.Error("providers failed", "err", err)
		}
		writeRouteError(w, http.StatusInternalServerError, "providers")
	}
}
