package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// ChallengeCookie is the WebAuthn challenge cookie.
const ChallengeCookie = "__Host-upmo_challenge"

const (
	invalidLink = `{"error":"invalid link"}` + "\n"
	signInAgain = `{"error":"sign in again"}` + "\n"
	lastPasskey = `{"error":"the last way to sign in cannot be removed"}` + "\n"
	slowDown    = `{"error":"slow down"}` + "\n"
)

// Passkeys serves passkey sign-in, links, passwords and passkey changes.
type Passkeys struct {
	Pass     app.Passkeys
	Password app.Passwords
	Trusted  []*net.IPNet
}

// Mount registers the passkey routes.
func (h Passkeys) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/passkeys/sign-in/options", h.limited(h.signInOptions))
	mux.HandleFunc("POST /api/passkeys/sign-in/finish", h.limited(h.signInFinish))
	mux.HandleFunc("POST /api/passkeys/link/options", h.limited(h.linkOptions))
	mux.HandleFunc("POST /api/passkeys/link/finish", h.limited(h.linkFinish))
	mux.HandleFunc("POST /api/passkeys/step-up/options", h.stepUpOptions)
	mux.HandleFunc("POST /api/passkeys/step-up/finish", h.stepUpFinish)
	mux.HandleFunc("GET /api/passkeys", h.list)
	mux.HandleFunc("POST /api/passkeys/options", h.addOptions)
	mux.HandleFunc("POST /api/passkeys/finish", h.addFinish)
	mux.HandleFunc("PATCH /api/passkeys/{id}", h.rename)
	mux.HandleFunc("DELETE /api/passkeys/{id}", h.remove)
	mux.HandleFunc("GET /api/sessions", h.devices)
	mux.HandleFunc("DELETE /api/sessions/{id}", h.endDevice)
	mux.HandleFunc("POST /api/sign-in/password", h.limited(h.passwordSignIn))
	mux.HandleFunc("PUT /api/password", h.passwordSet)
	mux.HandleFunc("DELETE /api/password", h.passwordClear)
	mux.HandleFunc("POST /api/session/verify/password", h.passwordVerify)
}

func (h Passkeys) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		allowed, retry, err := h.Pass.Store.Take(r.Context(), domain.ThrottleAddress(domain.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), h.Trusted)), passkeyNow(h.Pass), domain.ThrottleBurst, domain.ThrottlePerSecond)
		if err != nil {
			http.Error(w, "throttle", http.StatusInternalServerError)
			return
		}
		if !allowed {
			secs := int(retry.Round(time.Second) / time.Second)
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeRaw(w, http.StatusTooManyRequests, slowDown)
			return
		}
		next(w, r)
	}
}

func (h Passkeys) signInOptions(w http.ResponseWriter, r *http.Request) {
	cookie, options, err := h.Pass.BeginSignIn(r.Context())
	if err != nil {
		writeRaw(w, http.StatusBadRequest, unauthorised)
		return
	}
	setChallenge(w, cookie)
	writeRaw(w, http.StatusOK, string(options))
}

func (h Passkeys) signInFinish(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	text, err := h.Pass.FinishSignIn(r.Context(), challenge(r), body, agent(r))
	if err != nil {
		clearChallenge(w)
		if refuseSignedOut(w, err) {
			return
		}
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return
	}
	clearChallenge(w)
	SetCookie(w, text, int(h.Pass.Sessions.MaxAge.Seconds()))
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) linkOptions(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeRaw(w, http.StatusBadRequest, invalidLink)
		return
	}
	cookie, options, err := h.Pass.BeginLink(r.Context(), input.Token)
	if err != nil {
		writeRaw(w, http.StatusBadRequest, invalidLink)
		return
	}
	setChallenge(w, cookie)
	writeRaw(w, http.StatusOK, string(options))
}

func (h Passkeys) linkFinish(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token    string          `json:"token"`
		Response json.RawMessage `json:"response"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&input) != nil {
		writeRaw(w, http.StatusBadRequest, invalidLink)
		return
	}
	text, err := h.Pass.FinishLink(r.Context(), challenge(r), input.Token, input.Response, agent(r))
	if err != nil {
		clearChallenge(w)
		if refuseSignedOut(w, err) {
			return
		}
		writeRaw(w, http.StatusBadRequest, invalidLink)
		return
	}
	clearChallenge(w)
	SetCookie(w, text, int(h.Pass.Sessions.MaxAge.Seconds()))
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) stepUpOptions(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	cookie, options, err := h.Pass.BeginStepUp(r.Context(), session.UserID)
	if refuseSignedOut(w, err) {
		return
	}
	if err != nil {
		writeRaw(w, http.StatusBadRequest, unauthorised)
		return
	}
	setChallenge(w, cookie)
	writeRaw(w, http.StatusOK, string(options))
}

func (h Passkeys) stepUpFinish(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err := h.Pass.FinishStepUp(r.Context(), session.UserID, session.ID, challenge(r), body); err != nil {
		clearChallenge(w)
		if refuseSignedOut(w, err) {
			return
		}
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return
	}
	clearChallenge(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	rows, err := h.Pass.Store.ListPasskeys(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "passkeys", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"id":            row.ID,
			"name":          row.Name,
			"credential_id": base64.RawURLEncoding.EncodeToString(row.CredentialID),
			"added":         row.Added,
			"last_used":     row.LastUsed,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h Passkeys) addOptions(w http.ResponseWriter, r *http.Request) {
	session, ok := h.fresh(w, r)
	if !ok {
		return
	}
	cookie, options, err := h.Pass.BeginAdd(r.Context(), session.UserID)
	if refuseSignedOut(w, err) {
		return
	}
	if err != nil {
		writeRaw(w, http.StatusBadRequest, unauthorised)
		return
	}
	setChallenge(w, cookie)
	writeRaw(w, http.StatusOK, string(options))
}

func (h Passkeys) addFinish(w http.ResponseWriter, r *http.Request) {
	session, ok := h.fresh(w, r)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err := h.Pass.FinishAdd(r.Context(), session.UserID, session.ID, challenge(r), body); err != nil {
		clearChallenge(w)
		if refuseSignedOut(w, err) {
			return
		}
		writeRaw(w, http.StatusBadRequest, unauthorised)
		return
	}
	clearChallenge(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) rename(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Name == "" {
		writeRaw(w, http.StatusBadRequest, unauthorised)
		return
	}
	if err := h.Pass.Store.RenamePasskey(r.Context(), session.UserID, r.PathValue("id"), input.Name); err != nil {
		writeRaw(w, http.StatusNotFound, unauthorised)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) remove(w http.ResponseWriter, r *http.Request) {
	session, ok := h.fresh(w, r)
	if !ok {
		return
	}
	credentialID, err := h.Pass.Store.DeletePasskey(r.Context(), session.UserID, r.PathValue("id"))
	if errors.Is(err, app.ErrLastPasskey) {
		writeRaw(w, http.StatusConflict, lastPasskey)
		return
	}
	if err != nil {
		writeRaw(w, http.StatusNotFound, unauthorised)
		return
	}
	if err := h.Pass.Sessions.EndOthers(r.Context(), session.UserID, session.ID); err != nil {
		http.Error(w, "sessions", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"credential_id": base64.RawURLEncoding.EncodeToString(credentialID),
	})
}

func (h Passkeys) devices(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	rows, err := h.Pass.Sessions.Devices(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "sessions", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"id":             row.ID,
			"sign_in_method": row.Method,
			"signed_in_at":   row.SignedIn,
			"last_seen":      row.LastSeen,
			"user_agent":     row.Agent,
			"current":        row.ID == session.ID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h Passkeys) endDevice(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	rows, err := h.Pass.Sessions.Devices(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "sessions", http.StatusInternalServerError)
		return
	}
	owned := false
	for _, row := range rows {
		if row.ID == id {
			owned = true
		}
	}
	if !owned {
		writeRaw(w, http.StatusNotFound, unauthorised)
		return
	}
	if err := h.Pass.Sessions.End(r.Context(), id); err != nil {
		http.Error(w, "sessions", http.StatusInternalServerError)
		return
	}
	if id == session.ID {
		ClearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Passkeys) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return app.Session{}, false
	}
	session, err := h.Pass.Sessions.Lookup(r.Context(), cookie.Value)
	if err != nil {
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return app.Session{}, false
	}
	return session, true
}

func (h Passkeys) fresh(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	session, ok := h.session(w, r)
	if !ok {
		return app.Session{}, false
	}
	if !h.Pass.Fresh(session) {
		writeRaw(w, http.StatusForbidden, signInAgain)
		return app.Session{}, false
	}
	return session, true
}

func refuseSignedOut(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, app.ErrNotSignedIn) {
		return false
	}
	ClearCookie(w)
	writeRaw(w, http.StatusUnauthorized, unauthorised)
	return true
}

func passkeyNow(p app.Passkeys) time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func challenge(r *http.Request) string {
	cookie, err := r.Cookie(ChallengeCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func setChallenge(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: ChallengeCookie, Value: value, Path: "/", MaxAge: int(domain.ChallengeLifetime.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearChallenge(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: ChallengeCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func agent(r *http.Request) string {
	value := r.UserAgent()
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func writeRaw(w http.ResponseWriter, status int, body string) {
	if len(body) > 0 && body[0] == '{' {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
