//go:build integration

package identity_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestRolesIntegration(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	ownerPool, pool, handler, passkeys, ctx := identityMuxWithOwner(t, now)
	mux := handler.(*http.ServeMux)
	members := identity.Members(pool, sealer.NewEmail(t), "", nil, nil)
	members.Now = func() time.Time { return now }
	roles := identity.Roles(pool, nil)
	roles.Now = members.Now
	identity.MountMembers(mux, members, passkeys.Sessions)
	identity.MountRoles(mux, roles, passkeys.Sessions)

	link, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := finishLink(t, mux, soft, link)

	var memberRole, ownerRole string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRole); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'owner'`).Scan(&ownerRole); err != nil {
		t.Fatal(err)
	}

	created := call(mux, http.MethodPost, "/api/roles", ownerCookie, jsonBody(t, map[string]any{
		"name": "Clerk", "permissions": []string{"roles:manage", "task:view"},
	}))
	if created.Code != http.StatusOK {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var role struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &role); err != nil {
		t.Fatal(err)
	}

	listed := call(mux, http.MethodGet, "/api/roles", ownerCookie, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Clerk"`) || !strings.Contains(listed.Body.String(), "roles:manage") {
		t.Fatalf("list %d %s", listed.Code, listed.Body.String())
	}

	dup := call(mux, http.MethodPost, "/api/roles/"+ownerRole+"/duplicate", ownerCookie, jsonBody(t, map[string]string{"name": "Almost owner"}))
	if dup.Code != http.StatusOK {
		t.Fatalf("duplicate %d %s", dup.Code, dup.Body.String())
	}
	var copyID struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(dup.Body.Bytes(), &copyID); err != nil {
		t.Fatal(err)
	}
	var raw []string
	if err := pool.QueryRow(ctx, `SELECT permissions FROM identity.roles WHERE id = $1`, copyID.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, permission := range raw {
		if permission == string(domain.PermOwnersManage) {
			t.Fatal("duplicated owner held owners:manage")
		}
	}
	if len(raw) == 0 {
		t.Fatal("duplicate was empty")
	}

	system := call(mux, http.MethodDelete, "/api/roles/"+ownerRole, ownerCookie, nil)
	if system.Code != http.StatusConflict || !strings.Contains(system.Body.String(), "a system role cannot be changed") {
		t.Fatalf("system delete %d %s", system.Code, system.Body.String())
	}

	invited := call(mux, http.MethodPost, "/api/members", ownerCookie, jsonBody(t, map[string]string{
		"email": "bob@example.com", "name": "Bob", "role_id": memberRole,
	}))
	if invited.Code != http.StatusOK {
		t.Fatalf("invite %d %s", invited.Code, invited.Body.String())
	}
	var bob struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(invited.Body.Bytes(), &bob); err != nil {
		t.Fatal(err)
	}
	assigned := call(mux, http.MethodPut, "/api/members/"+bob.ID+"/role", ownerCookie, jsonBody(t, map[string]string{"role_id": role.ID}))
	if assigned.Code != http.StatusNoContent {
		t.Fatalf("assign %d %s", assigned.Code, assigned.Body.String())
	}
	held := call(mux, http.MethodDelete, "/api/roles/"+role.ID, ownerCookie, nil)
	if held.Code != http.StatusConflict || !strings.Contains(held.Body.String(), "held by 1 members") {
		t.Fatalf("in use %d %s", held.Code, held.Body.String())
	}

	bobSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	bobCookie := finishLink(t, mux, bobSoft, bob.Token)
	narrow := call(mux, http.MethodPost, "/api/roles", ownerCookie, jsonBody(t, map[string]any{
		"name": "Narrow", "permissions": []string{"roles:manage"},
	}))
	if narrow.Code != http.StatusOK {
		t.Fatalf("narrow %d %s", narrow.Code, narrow.Body.String())
	}
	var narrowRole struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(narrow.Body.Bytes(), &narrowRole); err != nil {
		t.Fatal(err)
	}
	caraInvite := call(mux, http.MethodPost, "/api/members", ownerCookie, jsonBody(t, map[string]string{
		"email": "cara@example.com", "name": "Cara", "role_id": memberRole,
	}))
	if caraInvite.Code != http.StatusOK {
		t.Fatalf("cara %d %s", caraInvite.Code, caraInvite.Body.String())
	}
	var cara struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(caraInvite.Body.Bytes(), &cara); err != nil {
		t.Fatal(err)
	}
	if rec := call(mux, http.MethodPut, "/api/members/"+cara.ID+"/role", ownerCookie, jsonBody(t, map[string]string{"role_id": narrowRole.ID})); rec.Code != http.StatusNoContent {
		t.Fatalf("assign cara %d %s", rec.Code, rec.Body.String())
	}
	caraSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	caraCookie := finishLink(t, mux, caraSoft, cara.Token)
	beyond := call(mux, http.MethodPut, "/api/roles/"+role.ID, caraCookie, jsonBody(t, map[string]any{
		"name": "Clerk", "permissions": []string{"roles:manage", "task:view", "task:delete"},
	}))
	if beyond.Code != http.StatusForbidden || !strings.Contains(beyond.Body.String(), "you cannot grant a permission you do not hold") {
		t.Fatalf("beyond %d %s", beyond.Code, beyond.Body.String())
	}
	ownRole := call(mux, http.MethodPut, "/api/roles/"+role.ID, bobCookie, jsonBody(t, map[string]any{
		"name": "Clerk", "permissions": []string{"roles:manage", "task:view"},
	}))
	if ownRole.Code != http.StatusConflict || !strings.Contains(ownRole.Body.String(), "you cannot do this to yourself") {
		t.Fatalf("own role %d %s", ownRole.Code, ownRole.Body.String())
	}
	self := call(mux, http.MethodPut, "/api/members/"+bob.ID+"/role", bobCookie, jsonBody(t, map[string]string{"role_id": memberRole}))
	if self.Code != http.StatusConflict || !strings.Contains(self.Body.String(), "you cannot do this to yourself") {
		t.Fatalf("self %d %s", self.Code, self.Body.String())
	}

	spare := call(mux, http.MethodPost, "/api/roles", ownerCookie, jsonBody(t, map[string]any{
		"name": "Spare", "permissions": []string{"task:view"},
	}))
	if spare.Code != http.StatusOK {
		t.Fatalf("spare %d %s", spare.Code, spare.Body.String())
	}
	var spareRole struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(spare.Body.Bytes(), &spareRole); err != nil {
		t.Fatal(err)
	}
	renamed := call(mux, http.MethodPut, "/api/roles/"+spareRole.ID, ownerCookie, jsonBody(t, map[string]any{
		"name": "Spare renamed", "permissions": []string{"task:view", "task:edit"},
	}))
	if renamed.Code != http.StatusNoContent {
		t.Fatalf("rename %d %s", renamed.Code, renamed.Body.String())
	}
	removed := call(mux, http.MethodDelete, "/api/roles/"+spareRole.ID, ownerCookie, nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("delete spare %d %s", removed.Code, removed.Body.String())
	}

	owner := userByEmail(t, pool, ctx, "ada@example.com")
	ownerID, err := identity.ParseUserID(owner)
	if err != nil {
		t.Fatal(err)
	}
	memberID, err := identity.ParseRoleID(memberRole)
	if err != nil {
		t.Fatal(err)
	}
	ownerSess, err := passkeys.Sessions.Lookup(ctx, ownerCookie)
	if err != nil {
		t.Fatal(err)
	}
	var secondOwnerText string
	if err := ownerPool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, joined_at)
		VALUES ('coowner@example.com', sha256('coowner@example.com'::bytea), 'k1', 'Co', (SELECT id FROM identity.roles WHERE key = 'owner'), $1, $1)
		RETURNING id::text`, now).Scan(&secondOwnerText); err != nil {
		t.Fatal(err)
	}
	secondOwnerID, err := identity.ParseUserID(secondOwnerText)
	if err != nil {
		t.Fatal(err)
	}
	_, secondOwnerSess, err := passkeys.Sessions.Start(ctx, secondOwnerID, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.Passkeys{Pool: pool}
	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	if _, err := tx1.Exec(ctx, `SELECT * FROM identity.member_set_role($1, $2, $3)`, ownerSess.TokenHash, secondOwnerID, memberID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMemberRole(ctx, secondOwnerSess.TokenHash, ownerID, memberID, nil); err != nil {
		t.Fatalf("second owner demote first: %v", err)
	}
	if err := tx1.Commit(ctx); err == nil || !strings.Contains(err.Error(), "install has no joined owner") {
		t.Fatalf("overlapping demote commit = %v, want last owner error", err)
	}
}

func TestRoleDeleteRefusedWhenShared(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	ownerPool, pool, handler, passkeys, ctx := identityMuxWithOwner(t, now)
	mux := handler.(*http.ServeMux)
	roles := identity.Roles(pool, nil)
	roles.Now = func() time.Time { return now }
	identity.MountRoles(mux, roles, passkeys.Sessions)

	link, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := finishLink(t, mux, soft, link)

	created := call(mux, http.MethodPost, "/api/roles", ownerCookie, jsonBody(t, map[string]any{
		"name": "Shared grantee", "permissions": []string{"task:view"},
	}))
	if created.Code != http.StatusOK {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var role struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &role); err != nil {
		t.Fatal(err)
	}

	ownerID := userByEmail(t, pool, ctx, "ada@example.com")
	if _, err := ownerPool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, grantee_role_id, level, granted_by)
		VALUES ('task', uuidv7(), $1, 'viewer', $2)`, role.ID, ownerID); err != nil {
		t.Fatal(err)
	}

	deleted := call(mux, http.MethodDelete, "/api/roles/"+role.ID, ownerCookie, nil)
	if deleted.Code != http.StatusConflict || !strings.Contains(deleted.Body.String(), "role is shared") {
		t.Fatalf("delete shared role %d %s", deleted.Code, deleted.Body.String())
	}
	var stillThere int
	if err := ownerPool.QueryRow(ctx, `SELECT count(*) FROM identity.roles WHERE id = $1`, role.ID).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 1 {
		t.Fatal("shared role was deleted")
	}
}
