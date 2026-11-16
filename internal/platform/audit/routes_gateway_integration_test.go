//go:build integration

package audit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	gatewayapp "github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

func TestGatewayRouteCreateUpdateShareDeleteSealAudit(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	team := newTeam(t, owner, "Finance")
	ctx := context.Background()

	provSvc := providerService(t, appPool, "settings:manage", []string{team})
	acct, err := provSvc.Add(ctx, providerOwnerID, gatewayapp.NewProvider{
		Provider:   "fake",
		Name:       "Workspace Fake",
		Adapter:    domain.AdapterFake,
		PlanKind:   domain.PlanMetered,
		BaseURL:    "https://ws.example",
		APIKey:     "sk-ws-secret",
		Models:     []string{"m1", "m2"},
		Visibility: domain.VisibilityEveryone,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed()

	routeSvc := gateway.NewRoutes(appPool, nil)
	routeSvc.Allow = func(context.Context, string) (string, error) { return domain.SharePermission, nil }
	routeSvc.Teams = func(context.Context, string) ([]string, error) { return []string{team}, nil }

	mux := http.NewServeMux()
	gateway.RoutesHandler{
		Book:     gateway.NewRouteBook(appPool),
		Service:  routeSvc,
		Accounts: provSvc,
		Access: func(r *http.Request) (string, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: providerOwnerID, CredentialType: "session"})
			return providerOwnerID, true
		},
	}.MountRoutes(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	// Create
	createBody := `{"name":"audit-route","capability":"chat","hops":[{"account_id":"` + acct.ID + `","model":"m1"}]}`
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/gateway/routes", strings.NewReader(createBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var made gateway.ListedRoute
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.RouteCreate || rows[0].TargetType != "route" || rows[0].TargetID != made.ID || rows[0].Reason != "" {
		t.Fatalf("create events = %+v", rows)
	}

	// Update
	updateBody := `{"name":"audit-route-v2","hops":[{"account_id":"` + acct.ID + `","model":"m2"}]}`
	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/gateway/routes/"+made.ID, strings.NewReader(updateBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.RouteUpdate || rows[0].TargetID != made.ID || rows[0].Reason != "" {
		t.Fatalf("update events = %+v", rows)
	}
	if strings.Join(rows[0].Changes.Fields, ",") != "hops,name" {
		t.Fatalf("update fields = %v, want hops,name", rows[0].Changes.Fields)
	}

	// Share
	shareBody := `{"visibility":"team","team_ids":["` + team + `"]}`
	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/gateway/routes/"+made.ID+"/share", strings.NewReader(shareBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("share = %d %s", rec.Code, rec.Body.String())
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.RouteShare || rows[0].TargetID != made.ID || rows[0].Reason != "" {
		t.Fatalf("share events = %+v", rows)
	}
	if got := changeOf(t, rows[0], "visibility"); got.From != domain.VisibilityPrivate || got.To != domain.VisibilityTeam {
		t.Fatalf("share visibility = %v -> %v", got.From, got.To)
	}
	if got := changeOf(t, rows[0], "team_ids"); !sameIDs(got.From) || !sameIDs(got.To, team) {
		t.Fatalf("share team_ids = %v -> %v", got.From, got.To)
	}

	// Delete
	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/gateway/routes/"+made.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.RouteDelete || rows[0].TargetID != made.ID || rows[0].Reason != "" {
		t.Fatalf("delete events = %+v", rows)
	}
}

func TestGatewayRouteUnauthorizedRecordsDenied(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	team := newTeam(t, owner, "Finance")
	ctx := context.Background()

	provSvc := providerService(t, appPool, "settings:manage", []string{team})
	acct, err := provSvc.Add(ctx, providerOwnerID, gatewayapp.NewProvider{
		Provider:   "fake",
		Name:       "Workspace Fake",
		Adapter:    domain.AdapterFake,
		PlanKind:   domain.PlanMetered,
		BaseURL:    "https://ws.example",
		APIKey:     "sk-ws-secret",
		Models:     []string{"m1"},
		Visibility: domain.VisibilityEveryone,
	})
	if err != nil {
		t.Fatal(err)
	}

	adminRoutes := gateway.NewRoutes(appPool, nil)
	adminRoutes.Allow = func(context.Context, string) (string, error) { return domain.SharePermission, nil }
	adminRoutes.Teams = func(context.Context, string) ([]string, error) { return []string{team}, nil }
	made, err := adminRoutes.Create(ctx, providerOwnerID, gatewayapp.NewRoute{
		Name:       "shared-route",
		Capability: "chat",
		Visibility: domain.VisibilityTeam,
		TeamIDs:    []string{team},
		Hops:       []gatewayapp.NewRouteHop{{AccountID: acct.ID, Model: "m1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed()

	memberRoutes := gateway.NewRoutes(appPool, nil)
	memberRoutes.Allow = func(context.Context, string) (string, error) { return "", nil }
	memberRoutes.Teams = func(context.Context, string) ([]string, error) { return []string{team}, nil }

	mux := http.NewServeMux()
	gateway.RoutesHandler{
		Book:    gateway.NewRouteBook(appPool),
		Service: memberRoutes,
		Access: func(r *http.Request) (string, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: providerMemberID, CredentialType: "session"})
			return providerMemberID, true
		},
	}.MountRoutes(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/gateway/routes/"+made.ID+"/share", strings.NewReader(`{"visibility":"everyone"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member share = %d %s, want 403", rec.Code, rec.Body.String())
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.RouteShare || rows[0].Outcome != audit.Denied || rows[0].ActorID != providerMemberID {
		t.Fatalf("denied events = %+v", rows)
	}
}
