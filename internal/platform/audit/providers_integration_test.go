//go:build integration

package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	gatewayapp "github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	providerOwnerID  = "018f3b2c-7a10-7c4a-8e11-777777777777"
	providerMemberID = "018f3b2c-7a10-7c4a-8e11-888888888888"
)

// providerRig migrates audit, identity and gateway on a fresh database and
// returns the owner pool and the request pool that runs as upmo_app.
func providerRig(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	return owner, pgtest.App(t, owner)
}

// providerService is the use case as brain wires it, on the request pool,
// with this person's sharing permission and teams held fixed.
func providerService(t *testing.T, appPool *pgxpool.Pool, allow string, teams []string) gatewayapp.Providers {
	t.Helper()
	keys, err := domain.NewKeyring("k1", map[string][]byte{"k1": bytes.Repeat([]byte{1}, domain.MasterKeyLen)})
	if err != nil {
		t.Fatal(err)
	}
	svc := gateway.NewProviders(appPool, keys, nil)
	svc.Allow = func(context.Context, string) (string, error) { return allow, nil }
	svc.Teams = func(context.Context, string) ([]string, error) { return teams, nil }
	svc.LocalPlan = func(context.Context, string) bool { return true }
	return svc
}

func newTeam(t *testing.T, owner *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	if err := owner.QueryRow(context.Background(),
		`INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// wholeRowJSON renders a sealed row with its changes, which containsSecret
// leaves out so a reason can carry content. A provider event must hold no
// key, no URL and no account name anywhere.
func wholeRowJSON(t *testing.T, row audit.Row) string {
	t.Helper()
	out, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func mustNotContain(t *testing.T, row audit.Row, secrets ...string) {
	t.Helper()
	encoded := wholeRowJSON(t, row)
	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("%s event holds %q: %s", row.Action, secret, encoded)
		}
	}
}

// sameIDs reports an audit value that lists exactly these team ids.
func sameIDs(value any, want ...string) bool {
	list, ok := value.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i, item := range list {
		if id, _ := item.(string); id != want[i] {
			return false
		}
	}
	return true
}

func strPtr(v string) *string { return &v }

// failingProbe never reaches a provider: every test fails on the key.
type failingProbe struct{}

func (failingProbe) Probe(context.Context, gatewayapp.Attempt) ([]domain.SeenModel, domain.FailureClass, error) {
	return nil, domain.ClassCredential, nil
}

func addedAccount(t *testing.T, svc gatewayapp.Providers) domain.Provider {
	t.Helper()
	made, err := svc.Add(context.Background(), providerOwnerID, gatewayapp.NewProvider{
		Provider: "fake",
		Name:     "Own Account",
		Adapter:  domain.AdapterFake,
		PlanKind: domain.PlanMetered,
		BaseURL:  "https://own.example",
		APIKey:   "sk-own-key",
		Models:   []string{"m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return made
}

func TestProviderCreateSealsItsAction(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	team := newTeam(t, owner, "Finance")
	svc := providerService(t, appPool, "settings:manage", []string{team})
	ctx := context.Background()

	made, err := svc.Add(ctx, providerOwnerID, gatewayapp.NewProvider{
		Provider:   "fake",
		Name:       "Team Account",
		Adapter:    domain.AdapterFake,
		PlanKind:   domain.PlanMetered,
		BaseURL:    "https://team.example",
		APIKey:     "sk-created-key",
		Models:     []string{"m1"},
		Visibility: domain.VisibilityTeam,
		TeamIDs:    []string{team},
	})
	if err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProviderCreate {
		t.Fatalf("events = %v, want one provider.create", actions(rows))
	}
	row := rows[0]
	// The store recorded the event, so the middleware left no fallback.
	if row.Reason != "" {
		t.Fatalf("reason = %q, want the store's own event", row.Reason)
	}
	if row.TargetType != "provider" || row.TargetID != made.ID {
		t.Fatalf("target = %s %s, want the stored account", row.TargetType, row.TargetID)
	}
	if got := changeOf(t, row, "provider"); got.To != "fake" {
		t.Fatalf("provider = %v, want the account's provider", got.To)
	}
	if got := changeOf(t, row, "plan_kind"); got.To != domain.PlanMetered {
		t.Fatalf("plan_kind = %v, want the account's plan", got.To)
	}
	if got := changeOf(t, row, "visibility"); got.To != domain.VisibilityTeam {
		t.Fatalf("visibility = %v, want the shared visibility", got.To)
	}
	if got := changeOf(t, row, "team_ids"); !sameIDs(got.To, team) {
		t.Fatalf("team_ids = %v, want the shared team", got.To)
	}
	mustNotContain(t, row, "sk-created-key", "https://team.example", "Team Account")
}

func TestProviderUpdateSealsItsChangedFields(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	svc := providerService(t, appPool, "", nil)
	ctx := context.Background()

	made := addedAccount(t, svc)
	sealed()
	// A test of the saved account stores the status the probe found, so the
	// change below has a status to reset.
	if result, err := svc.Test(ctx, providerOwnerID, gatewayapp.TestRequest{ID: made.ID}); err != nil || !result.OK {
		t.Fatalf("test = %+v %v, want a working test", result, err)
	}
	if rows := sealed(); len(rows) != 1 || rows[0].Action != audit.ProviderTest {
		t.Fatalf("events = %v, want one provider.test", actions(rows))
	}

	changed, err := svc.Change(ctx, providerOwnerID, made.ID, gatewayapp.ProviderChange{
		Name:    strPtr("Second Name"),
		BaseURL: strPtr("https://second.example"),
		APIKey:  strPtr("sk-second-key"),
	})
	if err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProviderUpdate {
		t.Fatalf("events = %v, want one provider.update", actions(rows))
	}
	row := rows[0]
	if row.TargetID != changed.ID || row.Reason != "" {
		t.Fatalf("event = %s %s, want the store's own update", row.TargetID, row.Reason)
	}
	if strings.Join(row.Changes.Fields, ",") != "base_url,credential,name,status" {
		t.Fatalf("fields = %v, want the changed field names", row.Changes.Fields)
	}
	if got := changeOf(t, row, "status"); got.From != domain.StatusConnected || got.To != domain.StatusNotConnected {
		t.Fatalf("status = %v to %v, want the reset", got.From, got.To)
	}
	if len(row.Changes.Values) != 1 {
		t.Fatalf("values = %s, want the status alone", changesJSON(t, row))
	}
	mustNotContain(t, row, "sk-own-key", "sk-second-key",
		"https://own.example", "https://second.example", "Own Account", "Second Name")
}

func TestProviderShareSealsTheMove(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	team := newTeam(t, owner, "Ops")
	svc := providerService(t, appPool, "settings:manage", []string{team})
	ctx := context.Background()

	made := addedAccount(t, svc)
	sealed()

	teams := []string{team}
	if _, err := svc.Change(ctx, providerOwnerID, made.ID, gatewayapp.ProviderChange{
		Visibility: strPtr(domain.VisibilityTeam),
		TeamIDs:    &teams,
	}); err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 2 || rows[0].Action != audit.ProviderUpdate || rows[1].Action != audit.ProviderShare {
		t.Fatalf("events = %v, want the update and the share", actions(rows))
	}
	update, share := rows[0], rows[1]
	if strings.Join(update.Changes.Fields, ",") != "team_ids,visibility" {
		t.Fatalf("update fields = %v, want the sharing move", update.Changes.Fields)
	}
	if got := changeOf(t, update, "visibility"); got.From != domain.VisibilityPrivate || got.To != domain.VisibilityTeam {
		t.Fatalf("visibility = %v to %v, want the move", got.From, got.To)
	}
	if got := changeOf(t, update, "team_ids"); !sameIDs(got.From) || !sameIDs(got.To, team) {
		t.Fatalf("team_ids = %v to %v, want the move", got.From, got.To)
	}
	if strings.Join(share.Changes.Fields, ",") != "team_ids,visibility" {
		t.Fatalf("share fields = %v, want the move", share.Changes.Fields)
	}
	if got := changeOf(t, share, "visibility"); got.From != domain.VisibilityPrivate || got.To != domain.VisibilityTeam {
		t.Fatalf("share visibility = %v to %v, want the move", got.From, got.To)
	}
	mustNotContain(t, update, "sk-own-key", "Own Account")
	mustNotContain(t, share, "sk-own-key", "Own Account")
}

func TestProviderDeleteSealsItsAction(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	svc := providerService(t, appPool, "", nil)
	ctx := context.Background()

	made := addedAccount(t, svc)
	sealed()
	if err := svc.Remove(ctx, providerOwnerID, made.ID); err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProviderDelete {
		t.Fatalf("events = %v, want one provider.delete", actions(rows))
	}
	row := rows[0]
	if row.TargetType != "provider" || row.TargetID != made.ID || row.Reason != "" {
		t.Fatalf("event = %s %s %q, want the store's own delete", row.TargetType, row.TargetID, row.Reason)
	}
	mustNotContain(t, row, "sk-own-key", "https://own.example", "Own Account")
}

func TestProviderTestSealsItsOutcome(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	svc := providerService(t, appPool, "", nil)
	ctx := context.Background()

	made := addedAccount(t, svc)
	sealed()

	if result, err := svc.Test(ctx, providerOwnerID, gatewayapp.TestRequest{ID: made.ID}); err != nil || !result.OK {
		t.Fatalf("test = %+v %v, want a working test", result, err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProviderTest {
		t.Fatalf("events = %v, want one provider.test", actions(rows))
	}
	if rows[0].Outcome != audit.Success || rows[0].TargetID != made.ID || rows[0].Reason != "" {
		t.Fatalf("event = %s %s %q, want the store's own success", rows[0].Outcome, rows[0].TargetID, rows[0].Reason)
	}
	mustNotContain(t, rows[0], "sk-own-key", "https://own.example", "Own Account")

	// A probe that fails the key leaves a failure event.
	svc.Probes[domain.AdapterFake] = failingProbe{}
	if result, err := svc.Test(ctx, providerOwnerID, gatewayapp.TestRequest{ID: made.ID}); err != nil || result.OK {
		t.Fatalf("failing test = %+v %v, want a refusal", result, err)
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProviderTest || rows[0].Outcome != audit.Failure {
		t.Fatalf("events = %v, want one failed provider.test", actions(rows))
	}
	mustNotContain(t, rows[0], "sk-own-key")
}

func TestProviderRouteSealsWithThePersonActor(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	svc := providerService(t, appPool, "", nil)

	mux := http.NewServeMux()
	gateway.ProvidersHandler{
		StepUp:  func(*http.Request) bool { return true },
		Service: svc,
		Access: func(r *http.Request) (string, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: providerOwnerID, CredentialType: "session"})
			return providerOwnerID, true
		},
	}.MountProviders(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	body := `{"provider":"fake","name":"Routed Account","adapter":"fake","plan_kind":"metered","base_url":"https://routed.example","api_key":"sk-routed-key"}`
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/providers", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProviderCreate {
		t.Fatalf("events = %v, want one provider.create", actions(rows))
	}
	row := rows[0]
	// The adapter recorded the event, so the middleware left no fallback.
	if row.Reason != "" {
		t.Fatalf("reason = %q, want the store's own event", row.Reason)
	}
	if row.ActorType != "user" || row.ActorID != providerOwnerID || row.CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.CredentialType)
	}
	mustNotContain(t, row, "sk-routed-key", "https://routed.example", "Routed Account")
}

func TestProviderUpdateRefusedForMemberRecordsDenied(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	ctx := context.Background()
	team := newTeam(t, owner, "Finance")

	admin := providerService(t, appPool, "settings:manage", nil)
	made, err := admin.Add(ctx, providerOwnerID, gatewayapp.NewProvider{
		Provider:   "fake",
		Name:       "Team Account",
		Adapter:    domain.AdapterFake,
		PlanKind:   domain.PlanMetered,
		BaseURL:    "https://team.example",
		APIKey:     "sk-team-key",
		Visibility: domain.VisibilityTeam,
		TeamIDs:    []string{team},
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed()

	// A member of the team may use the account, but a shared one is managed
	// by the people who hold settings:manage.
	member := providerService(t, appPool, "", []string{team})
	mux := http.NewServeMux()
	gateway.ProvidersHandler{
		StepUp:  func(*http.Request) bool { return true },
		Service: member,
		Access: func(r *http.Request) (string, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: providerMemberID, CredentialType: "session"})
			return providerMemberID, true
		},
	}.MountProviders(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/providers/"+made.ID, strings.NewReader(`{"name":"Renamed"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member change = %d %s, want 403", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProviderUpdate {
		t.Fatalf("events = %v, want one denied provider.update", actions(rows))
	}
	if rows[0].Outcome != audit.Denied || rows[0].ActorID != providerMemberID {
		t.Fatalf("event = %s %s %s, want the refused member denied", rows[0].Action, rows[0].Outcome, rows[0].ActorID)
	}
	mustNotContain(t, rows[0], "Renamed")
}

func TestProviderModelsReadSealsAndRefusesInvisibleAccount(t *testing.T) {
	owner, appPool := providerRig(t)
	sealed := newSealer(t, owner)
	svc := providerService(t, appPool, "", nil)
	ctx := context.Background()

	made := addedAccount(t, svc)
	if result, err := svc.Test(ctx, providerOwnerID, gatewayapp.TestRequest{ID: made.ID}); err != nil || !result.OK {
		t.Fatalf("test = %+v %v, want a working test", result, err)
	}
	sealed()

	mountAs := func(user string) http.Handler {
		mux := http.NewServeMux()
		gateway.ProvidersHandler{
			StepUp:  func(*http.Request) bool { return true },
			Service: providerService(t, appPool, "", nil),
			Access: func(r *http.Request) (string, bool) {
				audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: user, CredentialType: "session"})
				return user, true
			},
		}.MountProviders(mux, nil)
		return serveWithAudit(t, mux, appPool)
	}

	// An authorized read succeeds and seals one provider.models.read event.
	rec := httptest.NewRecorder()
	mountAs(providerOwnerID).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers/"+made.ID+"/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner read models = %d %s", rec.Code, rec.Body.String())
	}
	var got []domain.SeenModel
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) == 0 {
		t.Fatalf("owner models = %s, err = %v, want non-empty list", rec.Body.String(), err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProviderModelsRead {
		t.Fatalf("events = %v, want one provider.models.read", actions(rows))
	}
	row := rows[0]
	if row.Outcome != audit.Success || row.TargetType != "provider" || row.TargetID != made.ID || row.Reason != "" {
		t.Fatalf("event = %s %s %s %q, want the store's own read event", row.Outcome, row.TargetType, row.TargetID, row.Reason)
	}
	if row.ActorType != "user" || row.ActorID != providerOwnerID || row.CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.CredentialType)
	}
	mustNotContain(t, row, "sk-own-key", "https://own.example", "Own Account")

	// A person who may not see the account is refused with 404 and sees no rows.
	rec = httptest.NewRecorder()
	mountAs(providerMemberID).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers/"+made.ID+"/models", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stranger read models = %d %s, want 404", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), got[0].ID) || strings.Contains(rec.Body.String(), "sk-own-key") {
		t.Fatalf("refused response leaked rows or secret: %s", rec.Body.String())
	}
	if rows := sealed(); len(rows) != 0 {
		t.Fatalf("refused read recorded %v, want 0 events", actions(rows))
	}
}
