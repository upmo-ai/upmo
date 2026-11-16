// Package provision serves the signed /api/v1/provision/* control-plane HTTP API.
package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

const (
	maxBodyBytes     = 64 << 10
	maxPackBodyBytes = 1 << 20
)

// Key is one derived provisioning verification key with a non-secret identifier.
type Key struct {
	ID      string
	Derived []byte
}

// ParseKeys parses comma-separated/list of upmo_svc_ keys and derives the
// AudienceProvision HMAC key for each. An empty slice returns nil, nil
// (provisioning disabled).
func ParseKeys(raw []string) ([]Key, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]Key, 0, len(raw))
	for i, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		master, err := svcsig.ParseKey(s)
		if err != nil {
			return nil, fmt.Errorf("provision: key[%d]: %w", i, err)
		}
		derived, err := svcsig.Derive(master, svcsig.AudienceProvision)
		if err != nil {
			return nil, fmt.Errorf("provision: key[%d]: %w", i, err)
		}
		sum := sha256.Sum256(derived)
		out = append(out, Key{
			ID:      "k-" + hex.EncodeToString(sum[:6]),
			Derived: derived,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// Handler serves /api/v1/provision/* endpoints authenticated by svcsig
// AudienceProvision on the owner pool.
type Handler struct {
	Owner         *pgxpool.Pool
	Identity      identity.ProvisionStore
	MasterKeys    gateway.Keyring
	Keys          []Key
	PackTrustKeys func() (agents.TrustKeys, error)
	Log           *slog.Logger
	Now           func() time.Time
}

type keyIDContextKey struct{}

func withKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyIDContextKey{}, id)
}

func keyIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(keyIDContextKey{}).(string)
	return id
}

func (h Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now().UTC()
}

// Mount registers all /api/v1/provision/* routes on mux. When Keys is empty,
// every /api/v1/provision/* route returns 404.
func (h Handler) Mount(mux *http.ServeMux) {
	mux.Handle("/api/v1/provision/", h.guard(http.NotFoundHandler()))
	mux.Handle("POST /api/v1/provision/members", h.guard(http.HandlerFunc(h.createMember)))
	mux.Handle("PUT /api/v1/provision/members/{external_id}", h.guard(http.HandlerFunc(h.updateMember)))
	mux.Handle("DELETE /api/v1/provision/members/{external_id}", h.guard(http.HandlerFunc(h.deleteMember)))
	mux.Handle("POST /api/v1/provision/teams", h.guard(http.HandlerFunc(h.createTeam)))
	mux.Handle("PUT /api/v1/provision/teams/{external_id}", h.guard(http.HandlerFunc(h.updateTeam)))
	mux.Handle("DELETE /api/v1/provision/teams/{external_id}", h.guard(http.HandlerFunc(h.deleteTeam)))
	mux.Handle("POST /api/v1/provision/sign-in-links", h.guard(http.HandlerFunc(h.issueSignInLink)))
	mux.Handle("PUT /api/v1/provision/providers", h.guard(http.HandlerFunc(h.upsertProviders)))
	mux.Handle("PUT /api/v1/provision/quotas", h.guard(http.HandlerFunc(h.setQuota)))
	mux.Handle("DELETE /api/v1/provision/quotas", h.guard(http.HandlerFunc(h.clearQuota)))
	mux.Handle("PUT /api/v1/provision/packs", h.guard(http.HandlerFunc(h.installPack)))
	mux.Handle("GET /api/v1/provision/status", h.guard(http.HandlerFunc(h.status)))
	mux.Handle("GET /api/v1/provision/usage", h.guard(http.HandlerFunc(h.usage)))
}

func (h Handler) guard(next http.Handler) http.Handler {
	derived := make([][]byte, len(h.Keys))
	for i, k := range h.Keys {
		derived[i] = k.Derived
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(h.Keys) == 0 {
			http.NotFound(w, r)
			return
		}
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || bearer == "" {
			audit.Annotate(r.Context(), audit.Event{Reason: "missing_bearer"})
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		idx, err := svcsig.VerifyIndex(derived, svcsig.AudienceProvision, r.Method, r.URL.Path, bearer, h.now())
		if err != nil {
			audit.Annotate(r.Context(), audit.Event{Reason: "invalid_signature"})
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		kid := h.Keys[idx].ID
		audit.WithActor(r.Context(), audit.Actor{
			Type:           "system",
			CredentialType: audit.CredentialService,
		})
		next.ServeHTTP(w, r.WithContext(withKeyID(r.Context(), kid)))
	})
}

func (h Handler) createMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExternalID string   `json:"external_id"`
		Email      string   `json:"email"`
		Name       string   `json:"name"`
		Role       string   `json:"role"`
		Active     *bool    `json:"active"`
		Teams      []string `json:"teams"`
	}
	if !decodeStrictJSON(w, r, &in) {
		return
	}
	member, err := h.Identity.CreateMember(r.Context(), identity.ProvisionMemberInput{
		ExternalID: in.ExternalID,
		Email:      in.Email,
		Name:       in.Name,
		Role:       in.Role,
		Active:     in.Active,
		Teams:      in.Teams,
		KeyID:      keyIDFrom(r.Context()),
	})
	if err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, member)
}

func (h Handler) updateMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email  string   `json:"email"`
		Name   string   `json:"name"`
		Role   string   `json:"role"`
		Active *bool    `json:"active"`
		Teams  []string `json:"teams"`
	}
	if !decodeStrictJSON(w, r, &in) {
		return
	}
	member, err := h.Identity.UpdateMember(r.Context(), r.PathValue("external_id"), identity.ProvisionMemberUpdate{
		Email:  in.Email,
		Name:   in.Name,
		Role:   in.Role,
		Active: in.Active,
		Teams:  in.Teams,
		KeyID:  keyIDFrom(r.Context()),
	})
	if err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, member)
}

func (h Handler) deleteMember(w http.ResponseWriter, r *http.Request) {
	if err := h.Identity.DeleteMember(r.Context(), r.PathValue("external_id"), keyIDFrom(r.Context())); err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.NoContent(w)
}

func (h Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExternalID string   `json:"external_id"`
		Name       string   `json:"name"`
		Members    []string `json:"members"`
	}
	if !decodeStrictJSON(w, r, &in) {
		return
	}
	team, err := h.Identity.CreateTeam(r.Context(), identity.ProvisionTeamInput{
		ExternalID: in.ExternalID,
		Name:       in.Name,
		Members:    in.Members,
		KeyID:      keyIDFrom(r.Context()),
	})
	if err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, team)
}

func (h Handler) updateTeam(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if !decodeStrictJSON(w, r, &in) {
		return
	}
	team, err := h.Identity.UpdateTeam(r.Context(), r.PathValue("external_id"), identity.ProvisionTeamUpdate{
		Name:    in.Name,
		Members: in.Members,
		KeyID:   keyIDFrom(r.Context()),
	})
	if err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, team)
}

func (h Handler) deleteTeam(w http.ResponseWriter, r *http.Request) {
	if err := h.Identity.DeleteTeam(r.Context(), r.PathValue("external_id"), keyIDFrom(r.Context())); err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.NoContent(w)
}

func (h Handler) issueSignInLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		ExternalID string `json:"external_id"`
		Purpose    string `json:"purpose"`
	}
	if !decodeStrictJSON(w, r, &in) {
		return
	}
	link, err := h.Identity.IssueProvisionLink(r.Context(), in.ExternalID, in.Purpose, keyIDFrom(r.Context()))
	if err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, link)
}

func (h Handler) upsertProviders(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Providers []gateway.HouseProviderInput `json:"providers"`
		Name      string                       `json:"name"`
		Provider  string                       `json:"provider"`
		Adapter   string                       `json:"adapter"`
		PlanKind  string                       `json:"plan_kind"`
		BaseURL   string                       `json:"base_url"`
		APIKey    string                       `json:"api_key"`
		Models    []string                     `json:"models"`
	}
	if !decodeStrictJSON(w, r, &in) {
		return
	}
	inputs := in.Providers
	if inputs == nil && (in.Name != "" || in.Adapter != "" || in.PlanKind != "" || in.APIKey != "" || in.Models != nil) {
		inputs = []gateway.HouseProviderInput{{
			Name:     in.Name,
			Provider: in.Provider,
			Adapter:  in.Adapter,
			PlanKind: in.PlanKind,
			BaseURL:  in.BaseURL,
			APIKey:   in.APIKey,
			Models:   in.Models,
		}}
	}
	out, err := gateway.UpsertHouseProviders(r.Context(), h.Owner, h.MasterKeys, inputs, keyIDFrom(r.Context()))
	if err != nil {
		var invalid gateway.InvalidError
		switch {
		case errors.As(err, &invalid):
			writeError(w, http.StatusUnprocessableEntity, invalid.Error())
		case errors.Is(err, gateway.ErrNoMasterKey):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, gateway.ErrHouseProviderConflict):
			writeError(w, http.StatusConflict, err.Error())
		default:
			h.logErr("upsert providers failed", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (h Handler) setQuota(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SpendCeilingMicroUSD *int64 `json:"spend_ceiling_micro_usd"`
	}
	if !decodeStrictJSON(w, r, &in) {
		return
	}
	if in.SpendCeilingMicroUSD == nil {
		writeError(w, http.StatusUnprocessableEntity, "spend_ceiling_micro_usd is required")
		return
	}
	quota, err := h.Identity.SetSpendCeiling(r.Context(), *in.SpendCeilingMicroUSD, keyIDFrom(r.Context()))
	if err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, quota)
}

func (h Handler) clearQuota(w http.ResponseWriter, r *http.Request) {
	if err := h.Identity.ClearSpendCeiling(r.Context(), keyIDFrom(r.Context())); err != nil {
		h.writeIdentityErr(w, err)
		return
	}
	httpserver.NoContent(w)
}

func (h Handler) installPack(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPackBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	loadKeys := h.PackTrustKeys
	if loadKeys == nil {
		loadKeys = func() (agents.TrustKeys, error) {
			return agents.PackTrustKeysFromEnv(os.LookupEnv)
		}
	}
	trustKeys, err := loadKeys()
	if err != nil {
		h.logErr("load pack trust keys failed", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	pack, err := agents.VerifyPack(raw, trustKeys)
	if err != nil {
		switch {
		case errors.Is(err, agents.ErrMalformedPack):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, agents.ErrUntrustedKey):
			audit.Annotate(r.Context(), audit.Event{Reason: "untrusted_pack_key"})
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, agents.ErrInvalidSignature):
			audit.Annotate(r.Context(), audit.Event{Reason: "invalid_pack_signature"})
			writeError(w, http.StatusForbidden, err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	installed, err := agents.InstallPack(r.Context(), h.Owner, pack, keyIDFrom(r.Context()), h.now())
	if err != nil {
		var invalid agents.InvalidError
		switch {
		case errors.Is(err, agents.ErrAgentDuplicate),
			errors.Is(err, agents.ErrPackDowngrade):
			writeError(w, http.StatusConflict, err.Error())
		case errors.As(err, &invalid),
			errors.Is(err, agents.ErrLesson),
			errors.Is(err, agents.ErrLessonTitle),
			errors.Is(err, agents.ErrLessonCue),
			errors.Is(err, agents.ErrLessonBody),
			errors.Is(err, agents.ErrLessonDuplicate),
			errors.Is(err, agents.ErrLessonFull):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			h.logErr("install pack failed", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, installed)
}

func (h Handler) status(w http.ResponseWriter, r *http.Request) {
	if h.Owner == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if err := h.Owner.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	versions, err := migrate.SchemaVersions(r.Context(), h.Owner)
	if err != nil {
		h.logErr("provision status schema versions failed", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	ceiling, err := h.Identity.SpendCeiling(r.Context())
	if err != nil {
		h.logErr("provision status ceiling failed", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	info := buildinfo.Info()
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"ready":                   true,
		"version":                 info.Version,
		"commit":                  info.Commit,
		"build_time":              info.BuildTime,
		"schema_versions":         versions,
		"spend_ceiling_micro_usd": ceiling,
	})
}

func (h Handler) usage(w http.ResponseWriter, r *http.Request) {
	month := strings.TrimSpace(r.URL.Query().Get("month"))
	if month == "" {
		month = h.now().UTC().Format("2006-01")
	}
	if len(month) != 7 {
		writeError(w, http.StatusUnprocessableEntity, "month must be YYYY-MM")
		return
	}
	parsed, err := time.Parse("2006-01", month)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "month must be YYYY-MM")
		return
	}
	start := time.Date(parsed.Year(), parsed.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	summary, err := gateway.QueryMonthlyUsage(r.Context(), h.Owner, month, start, end)
	if err != nil {
		h.logErr("provision usage query failed", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, summary)
}

func (h Handler) writeIdentityErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrNoSuchMember),
		errors.Is(err, identity.ErrNoSuchTeam):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, identity.ErrExternalIDConflict),
		errors.Is(err, identity.ErrEmailConflict),
		errors.Is(err, identity.ErrTeamNameTaken),
		errors.Is(err, identity.ErrLastOwner):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, identity.ErrInvalidProvisionInput),
		errors.Is(err, identity.ErrUnknownTeamExternalID),
		errors.Is(err, identity.ErrUnknownMemberExternalID),
		errors.Is(err, identity.ErrMemberDisabled),
		errors.Is(err, identity.ErrBadTeam),
		errors.Is(err, identity.ErrInvalidQuota):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		h.logErr("provision identity error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (h Handler) logErr(msg string, err error) {
	if h.Log != nil {
		h.Log.Error(msg, "err", err)
	}
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad request")
		return false
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		writeError(w, http.StatusBadRequest, "bad request")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	httpserver.WriteJSON(w, status, map[string]string{"error": msg})
}
