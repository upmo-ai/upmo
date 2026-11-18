//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/agents/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestAgentNamesAreUniquePerOwnerWithoutRegardToCase(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	fields := func(name string) app.AgentFields { return app.AgentFields{Name: name, Enabled: true} }
	shared := func(name string) app.AgentFields {
		f := fields(name)
		f.Visibility = agents.VisibilityEveryone
		return f
	}

	if _, err := app.CreateAgent(ctx, store, nil, memberAccess, fields("Research")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CreateAgent(ctx, store, nil, memberAccess, fields("RESEARCH")); !errors.Is(err, app.ErrAgentDuplicate) {
		t.Fatalf("same name, other case: %v, want duplicate", err)
	}
	if _, err := app.CreateAgent(ctx, store, nil, otherAccess, fields("Research")); err != nil {
		t.Fatalf("the other person: %v", err)
	}
	if _, err := app.CreateAgent(ctx, store, nil, adminAccess, shared("research")); err != nil {
		t.Fatalf("a workspace agent with the name: %v", err)
	}
	if _, err := app.CreateAgent(ctx, store, nil, adminAccess, shared("RESEARCH")); !errors.Is(err, app.ErrAgentDuplicate) {
		t.Fatalf("two workspace agents: %v, want duplicate", err)
	}
	second, err := app.CreateAgent(ctx, store, nil, memberAccess, fields("Writing"))
	if err != nil {
		t.Fatal(err)
	}
	rename := "research"
	if _, err := app.ChangeAgent(ctx, store, nil, memberAccess, second.ID, app.AgentChange{Name: &rename}); !errors.Is(err, app.ErrAgentDuplicate) {
		t.Fatalf("rename: %v, want duplicate", err)
	}
}

func TestAgentRefusalsArePlainMessagesWithTheRightStatus(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	mux := http.NewServeMux()
	var access app.Access
	agents.Handler{
		Agents: store,
		Access: func(*http.Request) (app.Access, bool) { return access, true },
	}.Mount(mux, nil)

	post := func(a app.Access, body string) (int, string) {
		access = a
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agents", strings.NewReader(body)))
		var got struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return rec.Code, got.Error
	}

	if code, _ := post(memberAccess, `{"name":"Taken"}`); code != http.StatusCreated {
		t.Fatalf("first create = %d", code)
	}
	for name, tc := range map[string]struct {
		who    app.Access
		body   string
		status int
		text   string
	}{
		"no name":   {memberAccess, `{"name":" "}`, http.StatusUnprocessableEntity, "Give the agent a name."},
		"half pair": {memberAccess, `{"name":"a","model_provider":"OpenAI"}`, http.StatusUnprocessableEntity, "Give both a provider and a model, or neither."},
		"duplicate": {memberAccess, `{"name":"TAKEN"}`, http.StatusConflict, "An agent with that name already exists."},
		"managed":   {memberAccess, `{"name":"House","visibility":"everyone"}`, http.StatusForbidden, "This agent is managed by your workspace."},
		"no create": {app.Access{UserID: ownerUser, Allow: "agent:view"}, `{"name":"Nope"}`, http.StatusForbidden, "You cannot create an agent."},
	} {
		if code, text := post(tc.who, tc.body); code != tc.status || text != tc.text {
			t.Errorf("%s: status %d error %q, want %d %q", name, code, text, tc.status, tc.text)
		}
	}
}
