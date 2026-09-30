//go:build integration

package identity_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
)

func TestTeamsIntegration(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	pool, handler, passkeys, ctx := identityMux(t, now)
	mux := handler.(*http.ServeMux)
	members := identity.Members(pool, "", nil, nil)
	members.Now = func() time.Time { return now }
	teams := identity.Teams(pool, nil)
	teams.Now = members.Now
	identity.MountMembers(mux, members, passkeys.Sessions)
	identity.MountTeams(mux, teams, passkeys.Sessions)

	link, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := finishLink(t, mux, soft, link)

	var memberRole string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRole); err != nil {
		t.Fatal(err)
	}
	created := call(mux, http.MethodPost, "/api/teams", ownerCookie, jsonBody(t, map[string]string{"name": " Finance "}))
	if created.Code != http.StatusOK {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var team struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &team); err != nil {
		t.Fatal(err)
	}
	again := call(mux, http.MethodPost, "/api/teams", ownerCookie, jsonBody(t, map[string]string{"name": "finance"}))
	if again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), "a team with that name already exists") {
		t.Fatalf("duplicate %d %s", again.Code, again.Body.String())
	}
	listed := call(mux, http.MethodGet, "/api/teams", ownerCookie, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Finance"`) {
		t.Fatalf("list %d %s", listed.Code, listed.Body.String())
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
	early := call(mux, http.MethodPost, "/api/teams/"+team.ID+"/members", ownerCookie, jsonBody(t, map[string]string{"user_id": bob.ID}))
	if early.Code != http.StatusConflict || !strings.Contains(early.Body.String(), "not joined yet") {
		t.Fatalf("early %d %s", early.Code, early.Body.String())
	}
	bobSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	bobCookie := finishLink(t, mux, bobSoft, bob.Token)
	added := call(mux, http.MethodPost, "/api/teams/"+team.ID+"/members", ownerCookie, jsonBody(t, map[string]string{"user_id": bob.ID}))
	if added.Code != http.StatusNoContent {
		t.Fatalf("add %d %s", added.Code, added.Body.String())
	}
	body := call(mux, http.MethodGet, "/api/members", ownerCookie, nil)
	if body.Code != http.StatusOK || !strings.Contains(body.Body.String(), `"teams":["Finance"]`) {
		t.Fatalf("members %d %s", body.Code, body.Body.String())
	}
	refused := call(mux, http.MethodPost, "/api/teams", bobCookie, jsonBody(t, map[string]string{"name": "Ops"}))
	if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "you cannot manage teams") {
		t.Fatalf("refused %d %s", refused.Code, refused.Body.String())
	}
	removed := call(mux, http.MethodDelete, "/api/teams/"+team.ID+"/members/"+bob.ID, ownerCookie, nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("remove %d %s", removed.Code, removed.Body.String())
	}
}
