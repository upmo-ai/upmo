package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

const maxLimitBody = 16 << 10

// UsageLimitsHandler serves reading and updating monthly spend limits for
// signed-in people.
type UsageLimitsHandler struct {
	Service app.UsageLimits
	Access  func(r *http.Request) (userID string, ok bool)
	Log     *slog.Logger
}

// MountLimits registers the usage limit routes. guard runs first.
func (h UsageLimitsHandler) MountLimits(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/usage/limits", wrap(http.HandlerFunc(h.get)))
	mux.Handle("PUT /api/usage/limits", wrap(http.HandlerFunc(h.set)))
}

type optionalInt64 struct {
	Set   bool
	Value *int64
}

func (o *optionalInt64) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.Value = nil
		return nil
	}
	var v int64
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

func signedInPerson(w http.ResponseWriter, r *http.Request, access func(r *http.Request) (string, bool)) (string, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeRouteError(w, http.StatusForbidden, "needs a signed-in person")
		return "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	if access == nil {
		writeRouteError(w, http.StatusUnauthorized, "unauthorised")
		return "", false
	}
	userID, ok := access(r)
	if !ok || userID == "" {
		writeRouteError(w, http.StatusUnauthorized, "unauthorised")
		return "", false
	}
	return userID, true
}

func (h UsageLimitsHandler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := signedInPerson(w, r, h.Access)
	if !ok {
		return
	}
	got, err := h.Service.Get(r.Context(), userID)
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, got)
}

func (h UsageLimitsHandler) set(w http.ResponseWriter, r *http.Request) {
	userID, ok := signedInPerson(w, r, h.Access)
	if !ok {
		return
	}
	var body struct {
		Kind           string        `json:"kind"`
		SubjectID      string        `json:"subject_id"`
		AmountMicroUSD optionalInt64 `json:"amount_micro_usd"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLimitBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeRouteError(w, http.StatusRequestEntityTooLarge, "That request is too large.")
			return
		}
		writeRouteError(w, http.StatusBadRequest, "bad request")
		return
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		writeRouteError(w, http.StatusBadRequest, "bad request")
		return
	}
	if !body.AmountMicroUSD.Set {
		writeRouteError(w, http.StatusUnprocessableEntity, "amount_micro_usd is required")
		return
	}
	updated, err := h.Service.Set(r.Context(), userID, app.SetUsageLimit{
		Kind:           body.Kind,
		SubjectID:      body.SubjectID,
		AmountMicroUSD: body.AmountMicroUSD.Value,
	})
	if err != nil {
		h.refuse(w, err)
		return
	}
	writeRouteJSON(w, http.StatusOK, updated)
}

func (h UsageLimitsHandler) refuse(w http.ResponseWriter, err error) {
	var invalid domain.Invalid
	switch {
	case errors.As(err, &invalid):
		writeRouteError(w, http.StatusUnprocessableEntity, string(invalid))
	case errors.Is(err, app.ErrLimitForbidden):
		writeRouteError(w, http.StatusForbidden, "Only workspace admins can change spend limits.")
	case errors.Is(err, app.ErrCeilingForbidden):
		writeRouteError(w, http.StatusForbidden, "The workspace ceiling can only be changed by the control plane.")
	default:
		if h.Log != nil {
			h.Log.Error("usage limits failed", "err", err)
		}
		writeRouteError(w, http.StatusInternalServerError, "usage limits")
	}
}
