package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

const wrongPassword = `{"error":"Email or password is wrong"}` + "\n"

func (h Passkeys) passwordSignIn(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&input) != nil {
		writeRaw(w, http.StatusUnauthorized, wrongPassword)
		return
	}
	token, offer, retry, err := h.Password.SignIn(r.Context(), input.Email, input.Password, domain.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), h.Trusted), agent(r))
	if writePasswordErr(w, retry, err) {
		return
	}
	SetCookie(w, token, int(h.Pass.Sessions.MaxAge.Seconds()))
	writeJSON(w, http.StatusOK, map[string]bool{"offer_passkey": offer})
}

func (h Passkeys) passwordSet(w http.ResponseWriter, r *http.Request) {
	session, ok := h.passwordFresh(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&input) != nil || input.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": domain.ErrPasswordShort.Error()})
		return
	}
	email, name, err := h.Password.Store.Profile(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "password", http.StatusInternalServerError)
		return
	}
	if err := h.Password.Set(r.Context(), session.UserID, session.ID, email, name, input.Password); err != nil {
		if writePasswordErr(w, 0, err) {
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) passwordClear(w http.ResponseWriter, r *http.Request) {
	session, ok := h.passwordFresh(w, r)
	if !ok {
		return
	}
	if err := h.Password.Clear(r.Context(), session.UserID, session.ID); writePasswordErr(w, 0, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) passwordVerify(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&input) != nil {
		writeRaw(w, http.StatusUnauthorized, wrongPassword)
		return
	}
	email, _, err := h.Password.Store.Profile(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "password", http.StatusInternalServerError)
		return
	}
	retry, err := h.Password.Verify(r.Context(), session.UserID, session.ID, email, input.Password, domain.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), h.Trusted))
	if writePasswordErr(w, retry, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) passwordFresh(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	session, ok := h.session(w, r)
	if !ok {
		return app.Session{}, false
	}
	if !h.Password.Fresh(session) {
		writeRaw(w, http.StatusForbidden, signInAgain)
		return app.Session{}, false
	}
	return session, true
}

func writePasswordErr(w http.ResponseWriter, retry time.Duration, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, app.ErrWrong):
		writeRaw(w, http.StatusUnauthorized, wrongPassword)
	case errors.Is(err, app.ErrLimited):
		secs := int(retry.Round(time.Second) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeRaw(w, http.StatusTooManyRequests, slowDown)
	case errors.Is(err, domain.ErrBusy):
		w.Header().Set("Retry-After", "1")
		writeRaw(w, http.StatusServiceUnavailable, `{"error":"try again"}`+"\n")
	case errors.Is(err, app.ErrLastPasskey):
		writeRaw(w, http.StatusConflict, lastPasskey)
	case errors.Is(err, domain.ErrPasswordShort), errors.Is(err, domain.ErrPasswordLong), errors.Is(err, domain.ErrPasswordRepetitive), errors.Is(err, domain.ErrPasswordCommon), errors.Is(err, domain.ErrPasswordPersonal):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		http.Error(w, "password", http.StatusInternalServerError)
	}
	return true
}
