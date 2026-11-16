package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestMemberRefusals(t *testing.T) {
	actor, err := domain.ParseUserID("00000000-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	other, err := domain.ParseUserID("00000000-0000-7000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	memberRole, err := domain.ParseRoleID("00000000-0000-7000-8000-000000000010")
	if err != nil {
		t.Fatal(err)
	}
	ownerRole, err := domain.ParseRoleID("00000000-0000-7000-8000-000000000011")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	store := &memberFake{actor: actor, system: "owner", memberRole: memberRole, ownerRole: ownerRole, signedIn: now}
	mux := memberMux(store, actor, now, now)

	body := map[string]string{"email": "ada@example.com", "name": "Ada", "role_id": memberRole.String()}
	if rec := memberCall(mux, http.MethodPost, "/api/members", body, true); rec.Code != http.StatusOK {
		t.Fatalf("invite status = %d, body %s", rec.Code, rec.Body.String())
	} else if !strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("invite body = %s", rec.Body.String())
	}

	tests := []struct {
		name   string
		method string
		path   string
		cookie bool
		body   map[string]string
		setup  func(*memberFake)
		status int
		error  string
	}{
		{name: "signed out", method: http.MethodGet, path: "/api/members", status: http.StatusUnauthorized, error: "unauthorised"},
		{name: "stale", method: http.MethodPost, path: "/api/members", cookie: true, body: body, setup: func(f *memberFake) { f.signedIn = now.Add(-11 * time.Minute) }, status: http.StatusForbidden, error: "sign in again"},
		{name: "member cannot invite", method: http.MethodPost, path: "/api/members", cookie: true, body: body, setup: func(f *memberFake) { f.system = "member" }, status: http.StatusForbidden, error: "only owners and admins manage members"},
		{name: "admin cannot invite an owner", method: http.MethodPost, path: "/api/members", cookie: true, body: map[string]string{"email": "a@example.com", "name": "A", "role_id": ownerRole.String()}, setup: func(f *memberFake) { f.system = "admin" }, status: http.StatusForbidden, error: "only an owner can change the owner role"},
		{name: "already a member", method: http.MethodPost, path: "/api/members", cookie: true, body: map[string]string{"email": "taken@example.com", "name": "A", "role_id": memberRole.String()}, status: http.StatusConflict, error: "already a member"},
		{name: "missing fields", method: http.MethodPost, path: "/api/members", cookie: true, body: map[string]string{"email": "a@example.com"}, status: http.StatusBadRequest, error: "a person needs an email address, a name and a role"},
		{name: "self", method: http.MethodDelete, path: "/api/members/" + actor.String(), cookie: true, status: http.StatusConflict, error: "you cannot do this to yourself"},
		{name: "last owner", method: http.MethodDelete, path: "/api/members/" + other.String(), cookie: true, setup: func(f *memberFake) { f.removeErr = domain.ErrLastOwner }, status: http.StatusConflict, error: "the install keeps at least one owner"},
		{name: "no such member", method: http.MethodDelete, path: "/api/members/" + other.String(), cookie: true, setup: func(f *memberFake) { f.removeErr = domain.ErrNoSuchMember }, status: http.StatusNotFound, error: "no such member"},
		{name: "already joined", method: http.MethodPost, path: "/api/members/" + other.String() + "/invite-link", cookie: true, setup: func(f *memberFake) { f.invited = false }, status: http.StatusConflict, error: "already joined"},
		{name: "not joined yet", method: http.MethodPost, path: "/api/members/" + other.String() + "/reset-link", cookie: true, setup: func(f *memberFake) { f.invited = true }, status: http.StatusConflict, error: "not joined yet"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store.system = "owner"
			store.signedIn = now
			store.removeErr = nil
			store.invited = false
			if tc.setup != nil {
				tc.setup(store)
			}
			rec := memberCall(memberMux(store, actor, now, store.signedIn), tc.method, tc.path, tc.body, tc.cookie)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tc.status, rec.Body.String())
			}
			var payload struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error != tc.error {
				t.Fatalf("error = %q, want %q", payload.Error, tc.error)
			}
		})
	}
}

func memberMux(store *memberFake, actor domain.UserID, now, signed time.Time) *http.ServeMux {
	mux := http.NewServeMux()
	Members{
		Members:  app.Members{Store: store, Now: func() time.Time { return now }},
		Sessions: app.Service{Store: sessionFake{user: actor, at: signed}, Idle: domain.IdleTimeout, MaxAge: domain.MaxAge, Now: func() time.Time { return now }},
	}.Mount(mux)
	return mux
}

func memberCall(mux *http.ServeMux, method, path string, body map[string]string, cookie bool) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	if body == nil {
		raw = nil
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if cookie {
		_, text, err := domain.NewToken()
		if err != nil {
			panic(err)
		}
		req.AddCookie(&http.Cookie{
			Name: CookieName, Value: text, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type sessionFake struct {
	user domain.UserID
	at   time.Time
}

func (s sessionFake) Insert(context.Context, domain.UserID, []byte, time.Time, time.Time, string, string) (string, error) {
	return "", nil
}
func (s sessionFake) Find(context.Context, []byte) (app.Row, bool, error) {
	return app.Row{ID: "session", UserID: s.user, LastSeen: s.at, Expires: s.at.Add(time.Hour), Method: "passkey", SignedIn: s.at}, true, nil
}
func (s sessionFake) Touch(context.Context, string, time.Time) error { return nil }
func (s sessionFake) Delete(context.Context, string) error           { return nil }
func (s sessionFake) DeleteUser(context.Context, domain.UserID) error {
	return nil
}
func (s sessionFake) DeleteOthers(context.Context, domain.UserID, string) error { return nil }
func (s sessionFake) Refresh(context.Context, string, string, time.Time) error  { return nil }
func (s sessionFake) DeleteEnded(context.Context, domain.UserID, time.Time, time.Duration) error {
	return nil
}
func (s sessionFake) List(context.Context, domain.UserID) ([]app.Device, error) { return nil, nil }

type memberFake struct {
	actor      domain.UserID
	system     string
	signedIn   time.Time
	memberRole domain.RoleID
	ownerRole  domain.RoleID
	removeErr  error
	invited    bool
}

func (f *memberFake) ListRoles(context.Context) ([]domain.MemberRole, error) {
	return []domain.MemberRole{{ID: f.memberRole, Name: "Member", System: "member"}}, nil
}
func (f *memberFake) ListMembers(context.Context) ([]domain.Member, error) {
	return nil, nil
}
func (f *memberFake) MemberActor(context.Context, domain.UserID) (domain.RoleID, string, bool, []string, error) {
	return f.ownerRole, f.system, true, nil, nil
}
func (f *memberFake) MemberTeams(context.Context, domain.UserID) ([]string, error) {
	return nil, nil
}
func (f *memberFake) SharedObjects(context.Context, domain.ObjectType, domain.UserID, []string, domain.RoleID) ([]app.ObjectShare, error) {
	return nil, nil
}
func (f *memberFake) TaskGrants(context.Context, string) ([]app.TaskGrant, error) {
	return nil, nil
}
func (f *memberFake) ReplaceTaskGrants(context.Context, string, []app.GrantWrite, domain.UserID) error {
	return nil
}
func (f *memberFake) ReplaceTaskGrantsTx(context.Context, pgx.Tx, string, []app.GrantWrite, domain.UserID) error {
	return nil
}
func (f *memberFake) NamesOf(context.Context, []string, []string) (map[string]string, map[string]string, error) {
	return nil, nil, nil
}
func (f *memberFake) RoleSystem(_ context.Context, id domain.RoleID) (string, error) {
	if id == f.ownerRole {
		return "owner", nil
	}
	return "member", nil
}
func (f *memberFake) UserByEmail(context.Context, string) (domain.UserID, bool, error) {
	return domain.UserID{}, false, nil
}
func (f *memberFake) InviteMember(_ context.Context, _ []byte, email, name string, role domain.RoleID, _, _ []byte, _ time.Time, _ time.Time) (domain.Member, error) {
	if email == "taken@example.com" {
		return domain.Member{}, domain.ErrAlreadyMember
	}
	return domain.Member{ID: f.actor, Email: email, Name: name, RoleID: role, RoleName: "Member", System: "member", Invited: true}, nil
}
func (f *memberFake) RemoveMember(_ context.Context, _ []byte, _ domain.UserID, _ time.Time, rule func(string) error) error {
	if err := rule(f.system); err != nil {
		return err
	}
	return f.removeErr
}
func (f *memberFake) ReplaceMemberLink(_ context.Context, _ domain.UserID, _ string, _ []byte, _ time.Time, rule func(string, bool) error) error {
	return rule("member", f.invited)
}
func (f *memberFake) Profile(context.Context, domain.UserID) (string, string, error) {
	return "", "", nil
}
