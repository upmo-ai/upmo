//go:build integration

package identity_test

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/runjwt"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestConnectorGrants(t *testing.T) {
	pool, ctx := up(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(pool, time.Hour, 24*time.Hour)
	passkeys, err := identity.Passkeys(pool, sealer.NewEmail(t), sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	identity.MountAPITokens(mux, sessions, identity.APITokens(pool, log), log, nil, pool, nil)
	identity.MountConnectorGrants(mux, identity.ConnectorGrants(pool), sessions)
	httpapi.Passkeys{Pass: passkeys}.Mount(mux)

	token, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	cookie := finishLink(t, mux, soft, token)

	listed := call(mux, http.MethodGet, "/api/connector-grants", cookie, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "[]") {
		t.Fatalf("empty list = %d %s", listed.Code, listed.Body.String())
	}

	saved := call(mux, http.MethodPut, "/api/connector-grants/github", cookie, []byte(`{"resource_scope":"these","resource_ids":["upmo-ai/upmo"],"changes":"allow"}`))
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), `"changes":"allow"`) {
		t.Fatalf("put = %d %s", saved.Code, saved.Body.String())
	}

	again := call(mux, http.MethodGet, "/api/connector-grants", cookie, nil)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), "github") {
		t.Fatalf("list = %d %s", again.Code, again.Body.String())
	}

	grants := identity.ConnectorGrants(pool)
	userID := userByEmail(t, pool, ctx, "ada@example.com")
	uid, err := identity.ParseUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := grants.Decide(ctx, uid, "github", domain.ConnectorDelete, "upmo-ai/upmo")
	if err != nil || verdict != domain.ConnectorAsk {
		t.Fatalf("delete = %s %v", verdict, err)
	}
	verdict, err = grants.Decide(ctx, uid, "github", domain.ConnectorChange, "other")
	if err != nil || verdict != domain.ConnectorBlock {
		t.Fatalf("outside = %s %v", verdict, err)
	}
	verdict, err = grants.Decide(ctx, uid, "slack", domain.ConnectorChange, "c")
	if err != nil || verdict != domain.ConnectorAsk {
		t.Fatalf("default = %s %v", verdict, err)
	}

	bad := call(mux, http.MethodPut, "/api/connector-grants/GitHub", cookie, []byte(`{"resource_scope":"all"}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("slug = %d %s", bad.Code, bad.Body.String())
	}

	removed := call(mux, http.MethodDelete, "/api/connector-grants/github", cookie, nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", removed.Code, removed.Body.String())
	}

	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = 'k'
	}
	tokens, err := identity.NewRunTokens(runjwt.KeyPrefix+base64.RawStdEncoding.EncodeToString(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := tokens.Mint(identity.AudienceConnectors, uid, "33333333-3333-3333-3333-333333333333", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err := tokens.Verify(identity.AudienceConnectors, jwt, time.Now())
	if err != nil || got.User != uid.String() {
		t.Fatalf("jwt = %+v %v", got, err)
	}
}
