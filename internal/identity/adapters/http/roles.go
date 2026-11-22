package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Roles serves custom roles and member assignment.
type Roles struct {
	Roles    app.Roles
	Sessions app.Service
}

// Mount registers the role routes.
func (h Roles) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/roles", h.create)
	mux.HandleFunc("PUT /api/roles/{id}", h.change)
	mux.HandleFunc("POST /api/roles/{id}/duplicate", h.duplicate)
	mux.HandleFunc("DELETE /api/roles/{id}", h.delete)
	mux.HandleFunc("PUT /api/members/{id}/role", h.assign)
}

func (h Roles) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	var input struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeRaw(w, http.StatusBadRequest, memberError("a role needs a name and at least one permission"))
		return
	}
	id, err := h.Roles.Create(r.Context(), session, input.Name, input.Permissions)
	if refuseRole(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id.String()})
}

func (h Roles) change(w http.ResponseWriter, r *http.Request) {
	session, id, ok := h.roleID(w, r)
	if !ok {
		return
	}
	var input struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeRaw(w, http.StatusBadRequest, memberError("a role needs a name and at least one permission"))
		return
	}
	if refuseRole(w, h.Roles.Change(r.Context(), session, id, input.Name, input.Permissions)) {
		return
	}
	noContent(w)
}

func (h Roles) duplicate(w http.ResponseWriter, r *http.Request) {
	session, id, ok := h.roleID(w, r)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeRaw(w, http.StatusBadRequest, memberError("a role needs a name and at least one permission"))
		return
	}
	created, err := h.Roles.Duplicate(r.Context(), session, id, input.Name)
	if refuseRole(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": created.String()})
}

func (h Roles) delete(w http.ResponseWriter, r *http.Request) {
	session, id, ok := h.roleID(w, r)
	if !ok {
		return
	}
	if refuseRole(w, h.Roles.Delete(r.Context(), session, id)) {
		return
	}
	noContent(w)
}

func (h Roles) assign(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	member, err := domain.ParseUserID(r.PathValue("id"))
	if err != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such member"))
		return
	}
	var input struct {
		RoleID string `json:"role_id"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeRaw(w, http.StatusBadRequest, memberError("a role needs a name and at least one permission"))
		return
	}
	role, err := domain.ParseRoleID(input.RoleID)
	if err != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such role"))
		return
	}
	if refuseRole(w, h.Roles.Set(r.Context(), session, member, role)) {
		return
	}
	noContent(w)
}

func (h Roles) roleID(w http.ResponseWriter, r *http.Request) (app.Session, domain.RoleID, bool) {
	session, ok := h.session(w, r)
	if !ok {
		return app.Session{}, domain.RoleID{}, false
	}
	id, err := domain.ParseRoleID(r.PathValue("id"))
	if err != nil {
		writeRaw(w, http.StatusNotFound, memberError("no such role"))
		return app.Session{}, domain.RoleID{}, false
	}
	return session, id, true
}

func (h Roles) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
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

func refuseRole(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var holders *domain.RoleHolders
	if errors.As(err, &holders) {
		writeRaw(w, http.StatusConflict, memberError(fmt.Sprintf("this role is held by %d members", holders.Count)))
		return true
	}
	status, text, ok := roleStatus(err)
	if !ok {
		writeRaw(w, http.StatusInternalServerError, "roles")
		return true
	}
	writeRaw(w, status, memberError(text))
	return true
}

func roleStatus(err error) (int, string, bool) {
	switch {
	case errors.Is(err, app.ErrStale):
		return http.StatusForbidden, "sign in again", true
	case errors.Is(err, domain.ErrRolesOnly):
		return http.StatusForbidden, "you cannot manage roles", true
	case errors.Is(err, domain.ErrBeyondOwn):
		return http.StatusForbidden, "you cannot grant a permission you do not hold", true
	case errors.Is(err, domain.ErrNotPermitted):
		return http.StatusForbidden, "only owners and admins manage members", true
	case errors.Is(err, domain.ErrOwnersOnly):
		return http.StatusForbidden, "only an owner can change the owner role", true
	case errors.Is(err, domain.ErrSystemRole):
		return http.StatusConflict, "a system role cannot be changed", true
	case errors.Is(err, domain.ErrRoleNameTaken):
		return http.StatusConflict, "a role with that name already exists", true
	case errors.Is(err, domain.ErrRoleShared):
		return http.StatusConflict, "role is shared", true
	case errors.Is(err, domain.ErrSelf):
		return http.StatusConflict, "you cannot do this to yourself", true
	case errors.Is(err, domain.ErrLastOwner):
		return http.StatusConflict, "the install keeps at least one owner", true
	case errors.Is(err, domain.ErrBadRole):
		return http.StatusBadRequest, "a role needs a name and at least one permission", true
	case errors.Is(err, domain.ErrNoSuchRole):
		return http.StatusNotFound, "no such role", true
	case errors.Is(err, app.ErrNotSignedIn):
		return http.StatusUnauthorized, "unauthorized", true
	case errors.Is(err, domain.ErrNoSuchMember):
		return http.StatusNotFound, "no such member", true
	default:
		return 0, "", false
	}
}
