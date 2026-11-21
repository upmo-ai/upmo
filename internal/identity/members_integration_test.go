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
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestMembersIntegration(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	ownerPool, pool, handler, passkeys, ctx := identityMuxWithOwner(t, now)
	mux := handler.(*http.ServeMux)
	sent := &sentMail{}
	emails := sealer.NewEmail(t)
	members := identity.Members(pool, emails, "https://upmo.test", sent, nil)
	members.Now = func() time.Time { return now }
	identity.MountMembers(mux, members, passkeys.Sessions)
	store := postgres.Passkeys{Pool: pool, Emails: emails}

	link, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := finishLink(t, mux, soft, link)
	owner := userByEmail(t, pool, ctx, "ada@example.com")
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
	if want := "invite bob@example.com https://upmo.test/login/link#" + invited.Token; sent.last() != want {
		t.Fatalf("invite mail = %q, want %q", sent.last(), want)
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
	if err := ownerPool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, joined_at)
		VALUES ('view@example.com', sha256('view@example.com'::bytea), 'k1', 'View', (SELECT id FROM identity.roles WHERE key = 'viewer'), $1, $1)
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
	var resetBody struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(reset.Body.Bytes(), &resetBody); err != nil {
		t.Fatal(err)
	}
	if want := "recover bob@example.com https://upmo.test/login/link#" + resetBody.Token; resetBody.Token == "" || sent.last() != want {
		t.Fatalf("reset mail = %q, want %q", sent.last(), want)
	}
	recovered, err := members.Recover(ctx, " BOB@example.com ")
	if err != nil {
		t.Fatal(err)
	}
	if want := "recover bob@example.com https://upmo.test/login/link#" + recovered; sent.last() != want {
		t.Fatalf("recover mail = %q, want %q", sent.last(), want)
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

	ownerSess, err := passkeys.Sessions.Lookup(ctx, ownerCookie)
	if err != nil {
		t.Fatal(err)
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
	againMember, err := store.InviteMember(ctx, ownerSess.TokenHash, "bob@example.com", "Robert", roleID, handle, domain.Hash(token), now, now.Add(domain.InviteLifetime))
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

	var first, second error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		first = inviteOnce(ctx, store, ownerSess.TokenHash, "twin@example.com", roleID, now)
	}()
	go func() {
		defer wg.Done()
		second = inviteOnce(ctx, store, ownerSess.TokenHash, "twin@example.com", roleID, now)
	}()
	wg.Wait()
	if (first == nil) == (second == nil) || (!errors.Is(first, domain.ErrAlreadyMember) && !errors.Is(second, domain.ErrAlreadyMember)) {
		t.Fatalf("concurrent invite = %v, %v", first, second)
	}

	ownerID, err := identity.ParseUserID(owner)
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
	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	if _, err := tx1.Exec(ctx, `SELECT identity.member_disable($1, $2, $3)`, ownerSess.TokenHash, secondOwnerID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveMember(ctx, secondOwnerSess.TokenHash, ownerID, now, nil); err != nil {
		t.Fatalf("second owner remove first: %v", err)
	}
	if err := tx1.Commit(ctx); err == nil || !strings.Contains(err.Error(), "install has no joined owner") {
		t.Fatalf("overlapping remove commit = %v, want last owner error", err)
	}
	var ownersLeft int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE role.key = 'owner' AND person.joined_at IS NOT NULL AND person.disabled_at IS NULL`).Scan(&ownersLeft); err != nil {
		t.Fatal(err)
	}
	if ownersLeft != 1 {
		t.Fatalf("owners left = %d, want 1", ownersLeft)
	}
}

// sentMail records each link email as "purpose to link".
type sentMail struct {
	mu   sync.Mutex
	sent []string
}

func (s *sentMail) SendInvite(_ context.Context, to, link string) error {
	return s.add("invite " + to + " " + link)
}

func (s *sentMail) SendRecover(_ context.Context, to, link string) error {
	return s.add("recover " + to + " " + link)
}

func (s *sentMail) add(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, line)
	return nil
}

func (s *sentMail) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return ""
	}
	return s.sent[len(s.sent)-1]
}

func inviteOnce(ctx context.Context, store postgres.Passkeys, actorHash []byte, email string, role domain.RoleID, now time.Time) error {
	handle := make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		return err
	}
	token, _, err := domain.NewToken()
	if err != nil {
		return err
	}
	_, err = store.InviteMember(ctx, actorHash, email, "Twin", role, handle, domain.Hash(token), now, now.Add(domain.InviteLifetime))
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
