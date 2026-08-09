//go:build integration

package identity_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestMembersIntegration(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	pool, handler, passkeys, ctx := identityMux(t, now)
	mux := handler.(*http.ServeMux)
	members := identity.Members(pool)
	members.Now = func() time.Time { return now }
	identity.MountMembers(mux, members, passkeys.Sessions)
	store := postgres.Passkeys{Pool: pool}

	link, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := finishLink(t, mux, soft, link)
	var owner string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM identity.users WHERE lower(email) = 'ada@example.com'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	var memberRole string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRole); err != nil {
		t.Fatal(err)
	}

	rec := call(mux, http.MethodPost, "/api/members", ownerCookie, jsonBody(t, map[string]string{
		"email": "bob@example.com", "name": "Bob", "role_id": memberRole,
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("invite %d %s", rec.Code, rec.Body.String())
	}
	var invited struct {
		ID      string `json:"id"`
		Invited bool   `json:"invited"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &invited); err != nil {
		t.Fatal(err)
	}
	if !invited.Invited || invited.Token == "" {
		t.Fatalf("invite body %+v", invited)
	}
	bobSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	if finishLink(t, mux, bobSoft, invited.Token) == "" {
		t.Fatal("bob did not join")
	}
	listed := call(mux, http.MethodGet, "/api/members", ownerCookie, nil)
	if listed.Code != http.StatusOK || !containsJoined(t, listed.Body.Bytes(), invited.ID) {
		t.Fatalf("list %d %s", listed.Code, listed.Body.String())
	}

	self := call(mux, http.MethodDelete, "/api/members/"+owner, ownerCookie, nil)
	if self.Code != http.StatusConflict || !strings.Contains(self.Body.String(), "you cannot do this to yourself") {
		t.Fatalf("self %d %s", self.Code, self.Body.String())
	}

	var viewer string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, joined_at)
		VALUES ('view@example.com', 'View', (SELECT id FROM identity.roles WHERE key = 'viewer'), $1, $1)
		RETURNING id::text`, now).Scan(&viewer); err != nil {
		t.Fatal(err)
	}
	viewerID, err := identity.ParseUserID(viewer)
	if err != nil {
		t.Fatal(err)
	}
	viewerCookie, _, err := passkeys.Sessions.Start(ctx, viewerID, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	denied := call(mux, http.MethodPost, "/api/members", viewerCookie, jsonBody(t, map[string]string{
		"email": "cara@example.com", "name": "Cara", "role_id": memberRole,
	}))
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "only owners and admins manage members") {
		t.Fatalf("viewer invite %d %s", denied.Code, denied.Body.String())
	}

	if err := seedAccess(ctx, pool, owner, invited.ID, now); err != nil {
		t.Fatal(err)
	}
	reset := call(mux, http.MethodPost, "/api/members/"+invited.ID+"/reset-link", ownerCookie, nil)
	if reset.Code != http.StatusOK {
		t.Fatalf("reset %d %s", reset.Code, reset.Body.String())
	}
	again := call(mux, http.MethodPost, "/api/members/"+invited.ID+"/reset-link", ownerCookie, nil)
	if again.Code != http.StatusOK {
		t.Fatalf("second reset %d %s", again.Code, again.Body.String())
	}
	var unused int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.sign_in_links WHERE user_id = $1 AND used_at IS NULL`, invited.ID).Scan(&unused); err != nil {
		t.Fatal(err)
	}
	if unused != 1 {
		t.Fatalf("unused links = %d, want 1", unused)
	}

	removed := call(mux, http.MethodDelete, "/api/members/"+invited.ID, ownerCookie, nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("remove %d %s", removed.Code, removed.Body.String())
	}
	var disabled bool
	var passkeysLeft, sessionsLeft, sharesLeft int
	if err := pool.QueryRow(ctx, `
		SELECT disabled_at IS NOT NULL,
		       (SELECT count(*) FROM identity.passkeys WHERE user_id = $1),
		       (SELECT count(*) FROM identity.sessions WHERE user_id = $1),
		       (SELECT count(*) FROM identity.shares WHERE user_id = $1)
		FROM identity.users WHERE id = $1`, invited.ID).Scan(&disabled, &passkeysLeft, &sessionsLeft, &sharesLeft); err != nil {
		t.Fatal(err)
	}
	if !disabled || passkeysLeft != 0 || sessionsLeft != 0 || sharesLeft != 0 {
		t.Fatalf("cleanup disabled=%v passkeys=%d sessions=%d shares=%d", disabled, passkeysLeft, sessionsLeft, sharesLeft)
	}
	var granted int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.shares WHERE granted_by = $1`, invited.ID).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if granted != 1 {
		t.Fatalf("granted shares = %d, want 1", granted)
	}

	roleID, err := identity.ParseRoleID(memberRole)
	if err != nil {
		t.Fatal(err)
	}
	handle := make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		t.Fatal(err)
	}
	token, _, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	againMember, err := store.InviteMember(ctx, "bob@example.com", "Robert", roleID, handle, domain.Hash(token), now, now.Add(domain.InviteLifetime))
	if err != nil {
		t.Fatal(err)
	}
	if !againMember.Invited || againMember.Name != "Robert" {
		t.Fatalf("re-invite %+v", againMember)
	}
	var handleChanged bool
	if err := pool.QueryRow(ctx, `
		SELECT webauthn_handle = $2 AND disabled_at IS NULL AND joined_at IS NULL
		       AND (SELECT count(*) FROM identity.passkeys WHERE user_id = $1) = 0
		FROM identity.users WHERE id = $1`, againMember.ID, handle).Scan(&handleChanged); err != nil {
		t.Fatal(err)
	}
	if !handleChanged {
		t.Fatal("re-invite kept the old access")
	}

	ownerID, err := identity.ParseUserID(owner)
	if err != nil {
		t.Fatal(err)
	}
	err = store.RemoveMember(ctx, ownerID, now, func(string) error { return nil })
	if !errors.Is(err, domain.ErrLastOwner) {
		t.Fatalf("last owner err = %v", err)
	}
	var stillEnabled bool
	if err := pool.QueryRow(ctx, `SELECT disabled_at IS NULL FROM identity.users WHERE id = $1`, owner).Scan(&stillEnabled); err != nil {
		t.Fatal(err)
	}
	if !stillEnabled {
		t.Fatal("last owner was disabled")
	}

	var first, second error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		first = inviteOnce(ctx, store, "twin@example.com", roleID, now)
	}()
	go func() {
		defer wg.Done()
		second = inviteOnce(ctx, store, "twin@example.com", roleID, now)
	}()
	wg.Wait()
	if (first == nil) == (second == nil) || (!errors.Is(first, domain.ErrAlreadyMember) && !errors.Is(second, domain.ErrAlreadyMember)) {
		t.Fatalf("concurrent invite = %v, %v", first, second)
	}
}

func inviteOnce(ctx context.Context, store postgres.Passkeys, email string, role domain.RoleID, now time.Time) error {
	handle := make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		return err
	}
	token, _, err := domain.NewToken()
	if err != nil {
		return err
	}
	_, err = store.InviteMember(ctx, email, "Twin", role, handle, domain.Hash(token), now, now.Add(domain.InviteLifetime))
	return err
}

func seedAccess(ctx context.Context, pool *pgxpool.Pool, owner, member string, now time.Time) error {
	var team string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Finance') RETURNING id`).Scan(&team); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2)`, team, member); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', '00000000-0000-7000-8000-0000000000a1', $1, 'viewer', $2)`, member, owner); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', '00000000-0000-7000-8000-0000000000a2', $1, 'viewer', $2)`, owner, member); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO identity.passkeys (
		  user_id, credential_id, public_key, sign_count, aaguid, transports,
		  backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1, '\x11', '\x22', 0, '00000000-0000-7000-8000-0000000000a3', '{}', false, false, 'none', 'Phone', $2)`, member, now)
	return err
}

func jsonBody(t *testing.T, payload any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func containsJoined(t *testing.T, body []byte, id string) bool {
	t.Helper()
	var members []struct {
		ID      string `json:"id"`
		Invited bool   `json:"invited"`
	}
	if err := json.Unmarshal(body, &members); err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.ID == id {
			return !member.Invited
		}
	}
	return false
}

