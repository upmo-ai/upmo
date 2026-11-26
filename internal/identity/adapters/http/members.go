package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Members serves the people in the install.
type Members struct {
	Members  app.Members
	Sessions app.Service
}

// Mount registers the member routes.
func (h Members) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/roles", h.roles)
	mux.HandleFunc("GET /api/members", h.list)
	mux.HandleFunc("POST /api/members", h.invite)
	mux.HandleFunc("POST /api/members/{id}/invite-link", h.inviteLink)
	mux.HandleFunc("POST /api/members/{id}/reset-link", h.resetLink)
	mux.HandleFunc("DELETE /api/members/{id}", h.remove)
}

func (h Members) roles(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	roles, err := h.Members.Roles(r.Context(), session)
	if refuseMember(w, err) {
		return
	}
	out := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		names := make([]string, 0, len(role.Permissions))
		for _, permission := range role.Permissions {
			names = append(names, string(permission))
		}
		out = append(out, map[string]any{
			"id": role.ID.String(), "name": role.Name, "system": role.System,
			"permissions": names, "members": role.Members,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h Members) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	members, err := h.Members.List(r.Context(), session)
	if refuseMember(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, memberJSON(members))
}

func (h Members) invite(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	var input struct {
		Email  string `json:"email"`
		Name   string `json:"name"`
		RoleID string `json:"role_id"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeRaw(w, http.StatusBadRequest, memberError("a person needs an email address, a name and a role"))
		return
	}
	role, err := domain.ParseRoleID(strings.TrimSpace(input.RoleID))
	if err != nil {
		writeRaw(w, http.StatusBadRequest, memberError("a person needs an email address, a name and a role"))
		return
	}
	member, token, err := h.Members.Invite(r.Context(), session, input.Email, input.Name, role)
	if refuseMember(w, err) {
		return
	}
	body := memberJSON([]domain.Member{member})[0]
	body["token"] = token
	writeJSON(w, http.StatusOK, body)
}

func (h Members) inviteLink(w http.ResponseWriter, r *http.Request) {
	h.link(w, r, true)
}

func (h Members) resetLink(w http.ResponseWriter, r *http.Request) {
	h.link(w, r, false)
}

func (h Members) link(w http.ResponseWriter, r *http.Request, invite bool) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	id, err := domain.ParseUserID(r.PathValue("id"))
	if err != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such member"))
		return
	}
	var token string
	if invite {
		token, err = h.Members.InviteLink(r.Context(), session, id)
	} else {
		token, err = h.Members.ResetLink(r.Context(), session, id)
	}
	if refuseMember(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (h Members) remove(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	id, err := domain.ParseUserID(r.PathValue("id"))
	if err != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such member"))
		return
	}
	if err := h.Members.Remove(r.Context(), session, id); refuseMember(w, err) {
		return
	}
	noContent(w)
}

func (h Members) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	if !allowPerson(w, r) {
		return app.Session{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		writeRaw(w, http.StatusUnauthorized, unauthorized)
		return app.Session{}, false
	}
	session, err := h.Sessions.Lookup(r.Context(), cookie.Value)
	if err != nil {
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorized)
		return app.Session{}, false
	}
	markActor(r, session)
	return session, true
}

func memberJSON(members []domain.Member) []map[string]any {
	out := make([]map[string]any, 0, len(members))
	for _, member := range members {
		teams := member.Teams
		if teams == nil {
			teams = []string{}
		}
		out = append(out, map[string]any{
			"id":         member.ID.String(),
			"email":      member.Email,
			"name":       member.Name,
			"invited":    member.Invited,
			"teams":      teams,
			"avatar_url": member.AvatarURL,
			"role": map[string]string{
				"id":     member.RoleID.String(),
				"name":   member.RoleName,
				"system": member.System,
			},
		})
	}
	return out
}

func refuseMember(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	status, text, ok := memberStatus(err)
	if !ok {
		writeRaw(w, http.StatusInternalServerError, "members")
		return true
	}
	writeRaw(w, status, memberError(text))
	return true
}

func memberStatus(err error) (int, string, bool) {
	switch {
	case errors.Is(err, app.ErrStale):
		return http.StatusForbidden, "sign in again", true
	case errors.Is(err, domain.ErrNotPermitted):
		return http.StatusForbidden, "only owners and admins manage members", true
	case errors.Is(err, domain.ErrOwnersOnly):
		return http.StatusForbidden, "only an owner can change the owner role", true
	case errors.Is(err, domain.ErrAlreadyMember):
		return http.StatusConflict, "already a member", true
	case errors.Is(err, domain.ErrLastOwner):
		return http.StatusConflict, "the install keeps at least one owner", true
	case errors.Is(err, domain.ErrSelf):
		return http.StatusConflict, "you cannot do this to yourself", true
	case errors.Is(err, domain.ErrAlreadyJoined):
		return http.StatusConflict, "already joined", true
	case errors.Is(err, domain.ErrNotJoined):
		return http.StatusConflict, "not joined yet", true
	case errors.Is(err, domain.ErrMemberInput):
		return http.StatusBadRequest, "a person needs an email address, a name and a role", true
	case errors.Is(err, domain.ErrNoSuchMember), errors.Is(err, app.ErrNotSignedIn):
		return http.StatusNotFound, "no such member", true
	case errors.Is(err, app.ErrLimited):
		return http.StatusTooManyRequests, "slow down", true
	default:
		return 0, "", false
	}
}

func memberError(text string) string {
	return `{"error":"` + text + `"}` + "\n"
}
