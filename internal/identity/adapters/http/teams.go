package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Teams serves team creation and membership.
type Teams struct {
	Teams    app.Teams
	Sessions app.Service
}

// Mount registers the team routes.
func (h Teams) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/teams", h.list)
	mux.HandleFunc("POST /api/teams", h.create)
	mux.HandleFunc("POST /api/teams/{id}/members", h.add)
	mux.HandleFunc("DELETE /api/teams/{id}/members/{user}", h.remove)
}

func (h Teams) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r); !ok {
		return
	}
	teams, err := h.Teams.List(r.Context())
	if err != nil {
		writeRaw(w, http.StatusInternalServerError, "teams")
		return
	}
	out := make([]map[string]string, 0, len(teams))
	for _, team := range teams {
		out = append(out, map[string]string{"id": team.ID.String(), "name": team.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h Teams) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeRaw(w, http.StatusBadRequest, memberError("a team needs a name"))
		return
	}
	id, err := h.Teams.Create(r.Context(), session, input.Name)
	if refuseTeam(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id.String()})
}

func (h Teams) add(w http.ResponseWriter, r *http.Request) {
	session, team, ok := h.teamID(w, r)
	if !ok {
		return
	}
	var input struct {
		UserID string `json:"user_id"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such member"))
		return
	}
	user, err := domain.ParseUserID(input.UserID)
	if err != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such member"))
		return
	}
	if refuseTeam(w, h.Teams.Add(r.Context(), session, team, user)) {
		return
	}
	noContent(w)
}

func (h Teams) remove(w http.ResponseWriter, r *http.Request) {
	session, team, ok := h.teamID(w, r)
	if !ok {
		return
	}
	user, err := domain.ParseUserID(r.PathValue("user"))
	if err != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such member"))
		return
	}
	if refuseTeam(w, h.Teams.Remove(r.Context(), session, team, user)) {
		return
	}
	noContent(w)
}

func (h Teams) teamID(w http.ResponseWriter, r *http.Request) (app.Session, domain.TeamID, bool) {
	session, ok := h.session(w, r)
	if !ok {
		return app.Session{}, domain.TeamID{}, false
	}
	id, err := domain.ParseTeamID(r.PathValue("id"))
	if err != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such team"))
		return app.Session{}, domain.TeamID{}, false
	}
	return session, id, true
}

func (h Teams) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	if !allowPerson(w, r) {
		return app.Session{}, false
	}
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return app.Session{}, false
	}
	session, err := h.Sessions.Lookup(r.Context(), cookie.Value)
	if err != nil {
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return app.Session{}, false
	}
	return session, true
}

func refuseTeam(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	status, text, ok := teamStatus(err)
	if !ok {
		writeRaw(w, http.StatusInternalServerError, "teams")
		return true
	}
	writeRaw(w, status, memberError(text))
	return true
}

func teamStatus(err error) (int, string, bool) {
	switch {
	case errors.Is(err, app.ErrStale):
		return http.StatusForbidden, "sign in again", true
	case errors.Is(err, domain.ErrTeamsOnly):
		return http.StatusForbidden, "you cannot manage teams", true
	case errors.Is(err, domain.ErrTeamNameTaken):
		return http.StatusConflict, "a team with that name already exists", true
	case errors.Is(err, domain.ErrNotJoined):
		return http.StatusConflict, "not joined yet", true
	case errors.Is(err, domain.ErrBadTeam):
		return http.StatusBadRequest, "a team needs a name", true
	case errors.Is(err, domain.ErrNoSuchTeam):
		return http.StatusNotFound, "no such team", true
	case errors.Is(err, domain.ErrNoSuchMember):
		return http.StatusNotFound, "no such member", true
	default:
		return 0, "", false
	}
}
