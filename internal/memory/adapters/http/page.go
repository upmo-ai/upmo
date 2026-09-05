package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

// Handler serves the personalisation page for the signed-in person.
type Handler struct {
	Editor app.Editor
	User   func(r *http.Request) (string, bool)
	Now    func() time.Time
}

// Mount registers the personalisation routes. guard runs first, so a missing
// session never reaches the store.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/personalization", wrap(http.HandlerFunc(h.get)))
	mux.Handle("PUT /api/personalization/profile", wrap(http.HandlerFunc(h.saveProfile)))
	mux.Handle("POST /api/personalization/facts", wrap(http.HandlerFunc(h.addFact)))
	mux.Handle("PUT /api/personalization/facts/{id}", wrap(http.HandlerFunc(h.editFact)))
	mux.Handle("DELETE /api/personalization/facts/{id}", wrap(http.HandlerFunc(h.forget)))
}

func (h Handler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	page, err := app.LoadPage(r.Context(), h.Editor, userID)
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, pageJSON(page))
}

func (h Handler) saveProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		Body *string `json:"body"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Body == nil {
		writeError(w, http.StatusBadRequest, "the profile needs a body")
		return
	}
	if err := app.SaveProfile(r.Context(), h.Editor, userID, *input.Body, h.now()); refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"body": strings.TrimSpace(*input.Body)})
}

func (h Handler) addFact(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	claim, ok := claimBody(w, r)
	if !ok {
		return
	}
	id, err := app.AddFact(r.Context(), h.Editor, userID, claim, h.now())
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h Handler) editFact(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	claim, ok := claimBody(w, r)
	if !ok {
		return
	}
	if _, err := uuid.Parse(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "no such fact")
		return
	}
	id, err := app.EditFact(r.Context(), h.Editor, userID, r.PathValue("id"), claim, h.now())
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h Handler) forget(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.person(w, r)
	if !ok {
		return
	}
	if _, err := uuid.Parse(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "no such fact")
		return
	}
	if err := app.Forget(r.Context(), h.Editor, userID, r.PathValue("id"), h.now()); refuse(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (string, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.User == nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return "", false
	}
	userID, ok := h.User(r)
	if !ok || userID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return "", false
	}
	return userID, true
}

func (h Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func claimBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		Claim *string `json:"claim"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Claim == nil {
		writeError(w, http.StatusBadRequest, "a fact needs words")
		return "", false
	}
	return *input.Claim, true
}

func pageJSON(page app.Page) map[string]any {
	facts := make([]map[string]string, 0, len(page.Facts))
	for _, fact := range page.Facts {
		facts = append(facts, map[string]string{
			"id":           fact.ID,
			"claim":        fact.Claim,
			"trust":        fact.Trust,
			"confirmed_at": fact.Confirmed.UTC().Format(time.RFC3339),
		})
	}
	out := map[string]any{"profile": page.Profile, "facts": facts}
	if !page.ProfileAt.IsZero() {
		out["profile_at"] = page.ProfileAt.UTC().Format(time.RFC3339)
	}
	return out
}

func refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, app.ErrProfile):
		writeError(w, http.StatusBadRequest, "the profile is too long")
	case errors.Is(err, app.ErrClaim):
		writeError(w, http.StatusBadRequest, "a fact needs words")
	case errors.Is(err, app.ErrDuplicate):
		writeError(w, http.StatusConflict, "already remembered")
	case errors.Is(err, app.ErrGone):
		writeError(w, http.StatusNotFound, "no such fact")
	default:
		writeError(w, http.StatusInternalServerError, "personalisation")
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
