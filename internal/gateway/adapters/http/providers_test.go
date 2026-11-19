package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/fake"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

const savedID = "018f3b2c-7a10-7c4a-8e11-0000000000aa"

// savedAccount is an own metered account with an address and a key.
var savedAccount = domain.Provider{
	ID:            savedID,
	Provider:      "fake",
	Name:          "Saved",
	Adapter:       domain.AdapterFake,
	PlanKind:      domain.PlanMetered,
	BaseURL:       "https://saved.example",
	Status:        domain.StatusConnected,
	Models:        []string{"m1"},
	Own:           true,
	CredentialRef: "own_saved",
	HasKey:        true,
}

// recordedAccounts answers Get with the saved account and records what
// Change stores.
type recordedAccounts struct {
	updated   domain.Provider
	reset     bool
	updates   int
	created   int
	createRef string
}

func (a *recordedAccounts) List(context.Context, app.Caller) ([]domain.Provider, error) {
	return nil, nil
}

func (a *recordedAccounts) Get(_ context.Context, _ app.Caller, id string) (domain.Provider, error) {
	if id != savedID {
		return domain.Provider{}, app.ErrNotFound
	}
	return savedAccount, nil
}

func (a *recordedAccounts) Create(_ context.Context, _ app.Caller, p domain.Provider, _ *domain.Sealed) (domain.Provider, error) {
	a.created++
	a.createRef = p.CredentialRef
	return p, nil
}

func (a *recordedAccounts) Update(_ context.Context, _ app.Caller, p domain.Provider, _ *domain.Sealed, reset bool) (domain.Provider, error) {
	a.updates++
	a.updated = p
	a.reset = reset
	return p, nil
}

func (a *recordedAccounts) Remove(context.Context, app.Caller, string) error { return app.ErrNotFound }

func (a *recordedAccounts) Sealed(context.Context, app.Caller, string) (domain.Sealed, error) {
	return domain.Sealed{}, app.ErrNotFound
}

func (a *recordedAccounts) Note(context.Context, app.Caller, string, string) error { return nil }

func (a *recordedAccounts) PutSeenModels(context.Context, app.Caller, string, []domain.SeenModel) error {
	return nil
}

func (a *recordedAccounts) SeenModels(context.Context, app.Caller, string) ([]domain.SeenModel, error) {
	return nil, app.ErrNotFound
}

// providersService is the use case the tests drive. gate says who may use
// the local plan, as the brain wires it to the settings managers.
func providersService(t *testing.T, accounts app.Accounts, gate bool) app.Providers {
	t.Helper()
	keys, err := domain.NewKeyring("k1", map[string][]byte{"k1": bytes.Repeat([]byte{1}, domain.MasterKeyLen)})
	if err != nil {
		t.Fatal(err)
	}
	return app.Providers{
		Accounts: accounts,
		Keys:     keys,
		Known:    func(string) bool { return true },
		Probes:   map[string]app.Prober{domain.AdapterFake: fake.Adapter{}},
		LocalPlan: func(context.Context, string) bool {
			return gate
		},
	}
}

func callProviders(t *testing.T, service app.Providers, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	ProvidersHandler{
		Service: service,
		Access: func(*http.Request) (string, bool) {
			return "018f3b2c-7a10-7c4a-8e11-111111111111", true
		},
	}.MountProviders(mux, nil)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// A change that omits base_url keeps the saved address and leaves the status
// as it is.
func TestChangeWithoutABaseURLKeepsTheSavedOne(t *testing.T) {
	accounts := &recordedAccounts{}
	rec := callProviders(t, providersService(t, accounts, true), http.MethodPut, "/api/providers/"+savedID, `{"name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if accounts.updates != 1 || accounts.updated.BaseURL != savedAccount.BaseURL || accounts.reset {
		t.Fatalf("a missing base_url changed the address: base %q reset %v", accounts.updated.BaseURL, accounts.reset)
	}
}

// An explicit empty base_url clears the saved address, which resets the
// status to Not connected.
func TestChangeWithAnEmptyBaseURLClearsTheSavedOne(t *testing.T) {
	accounts := &recordedAccounts{}
	rec := callProviders(t, providersService(t, accounts, true), http.MethodPut, "/api/providers/"+savedID, `{"base_url":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if accounts.updates != 1 || accounts.updated.BaseURL != "" || !accounts.reset {
		t.Fatalf("an empty base_url did not clear: base %q reset %v", accounts.updated.BaseURL, accounts.reset)
	}
}

// A local-plan account reaches private addresses, so a person the brain does
// not count among the settings managers is refused with the plain sentence,
// while their metered account is still theirs to add.
func TestLocalPlanAddNeedsTheWorkspaceManagers(t *testing.T) {
	const localBody = `{"provider":"fake","name":"Local one","adapter":"fake","plan_kind":"local","models":["m1"]}`
	const sentence = "Only the workspace owner or an admin can use a provider on the local plan."

	accounts := &recordedAccounts{}
	rec := callProviders(t, providersService(t, accounts, false), http.MethodPost, "/api/providers", localBody)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), sentence) {
		t.Fatalf("member add: status %d body %s", rec.Code, rec.Body.String())
	}
	if accounts.created != 0 {
		t.Fatal("the refused account was stored")
	}

	accounts = &recordedAccounts{}
	rec = callProviders(t, providersService(t, accounts, true), http.MethodPost, "/api/providers", localBody)
	if rec.Code != http.StatusCreated || accounts.created != 1 {
		t.Fatalf("manager add: status %d body %s", rec.Code, rec.Body.String())
	}

	// The restriction is the local plan only: a metered account needs no
	// manager.
	rec = callProviders(t, providersService(t, accounts, false), http.MethodPost, "/api/providers",
		`{"provider":"fake","name":"Metered one","adapter":"fake","plan_kind":"metered","api_key":"sk-test","models":["m1"]}`)
	if rec.Code != http.StatusCreated || accounts.created != 2 {
		t.Fatalf("member metered add: status %d body %s", rec.Code, rec.Body.String())
	}
}

// Changing an account onto the local plan, or re-pointing one that is, is
// the act Add guards: a person the brain does not count among the settings
// managers is refused with the plain sentence, a manager succeeds, and a
// member's metered account is still theirs to edit.
func TestLocalPlanChangeNeedsTheWorkspaceManagers(t *testing.T) {
	const sentence = "Only the workspace owner or an admin can use a provider on the local plan."

	accounts := &recordedAccounts{}
	rec := callProviders(t, providersService(t, accounts, false), http.MethodPut, "/api/providers/"+savedID, `{"plan_kind":"local"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), sentence) {
		t.Fatalf("member change to local: status %d body %s", rec.Code, rec.Body.String())
	}
	if accounts.updates != 0 {
		t.Fatal("the refused change was stored")
	}

	accounts = &recordedAccounts{}
	rec = callProviders(t, providersService(t, accounts, true), http.MethodPut, "/api/providers/"+savedID, `{"plan_kind":"local"}`)
	if rec.Code != http.StatusOK || accounts.updates != 1 {
		t.Fatalf("manager change to local: status %d body %s", rec.Code, rec.Body.String())
	}

	// The restriction is the local plan only: a metered account is the
	// member's to edit without a manager.
	accounts = &recordedAccounts{}
	rec = callProviders(t, providersService(t, accounts, false), http.MethodPut, "/api/providers/"+savedID, `{"name":"Renamed"}`)
	if rec.Code != http.StatusOK || accounts.updates != 1 {
		t.Fatalf("member metered change: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestLocalPlanTestNeedsTheWorkspaceManagers(t *testing.T) {
	const testBody = `{"provider":"fake","adapter":"fake","plan_kind":"local","model":"m1"}`
	const sentence = "Only the workspace owner or an admin can use a provider on the local plan."

	rec := callProviders(t, providersService(t, &recordedAccounts{}, false), http.MethodPost, "/api/providers/test", testBody)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), sentence) {
		t.Fatalf("member test: status %d body %s", rec.Code, rec.Body.String())
	}

	rec = callProviders(t, providersService(t, &recordedAccounts{}, true), http.MethodPost, "/api/providers/test", testBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager test: status %d body %s", rec.Code, rec.Body.String())
	}
	var result app.TestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || !result.OK {
		t.Fatalf("manager test result = %s, %v", rec.Body.String(), err)
	}
}

// listedAccounts answers the list with fixed rows.
type listedAccounts struct {
	recordedAccounts
	rows []domain.Provider
}

func (f *listedAccounts) List(context.Context, app.Caller) ([]domain.Provider, error) {
	return f.rows, nil
}

// A person reads with every account which of its models they can use now: a
// Not connected account offers nothing, a Connected one lists the models it
// has.
func TestListProvidersCarriesTheUsableAnswer(t *testing.T) {
	broken := savedAccount
	broken.ID = "018f3b2c-7a10-7c4a-8e11-0000000000bb"
	broken.Name = "Broken"
	broken.Status = domain.StatusNotConnected
	accounts := &listedAccounts{rows: []domain.Provider{savedAccount, broken}}

	rec := callProviders(t, providersService(t, accounts, true), http.MethodGet, "/api/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []providerJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("list = %+v", got)
	}
	if !got[0].Usable || len(got[0].UsableModels) != 1 || got[0].UsableModels[0] != "m1" {
		t.Fatalf("the Connected account read %v with %v", got[0].Usable, got[0].UsableModels)
	}
	if got[1].Usable || got[1].UsableModels == nil || len(got[1].UsableModels) != 0 {
		t.Fatalf("the Not connected account read %v with %v", got[1].Usable, got[1].UsableModels)
	}
}

type fakeCatalogReader struct {
	rows map[string][]domain.CatalogRow
}

func (f fakeCatalogReader) VendorCatalog(_ context.Context, vendor string) ([]domain.CatalogRow, error) {
	return f.rows[vendor], nil
}

func (f fakeCatalogReader) CatalogModel(context.Context, string, string) (domain.CatalogRow, bool, error) {
	return domain.CatalogRow{}, false, nil
}

func (f fakeCatalogReader) ModelKinds(context.Context, []domain.VendorModel) (map[domain.VendorModel]domain.ModelKind, error) {
	return nil, nil
}

func TestVendorAndCatalogRoutesRequireSessionAndReturnData(t *testing.T) {
	svc := providersService(t, &recordedAccounts{}, true)
	inRate := 2.5
	outRate := 10.0
	svc.Catalog = fakeCatalogReader{rows: map[string][]domain.CatalogRow{
		"openai": {{
			Vendor:        "openai",
			Model:         "gpt-4o",
			Version:       "v1",
			Kind:          domain.KindChat,
			Inputs:        []string{domain.InputText, domain.InputImage},
			Outputs:       []string{domain.OutputText},
			Caps:          []string{domain.CapTools, domain.CapStructured},
			ContextWindow: 128000,
			MaxOutput:     16384,
			Price:         domain.Price{Version: "v1", InputPerMTok: &inRate, OutputPerMTok: &outRate},
		}},
	}}

	mux := http.NewServeMux()
	ProvidersHandler{
		Service: svc,
		Access: func(r *http.Request) (string, bool) {
			u := r.Header.Get("X-User")
			return u, u != ""
		},
	}.MountProviders(mux, nil)

	// Signed-out caller gets 401.
	for _, path := range []string{"/api/providers/vendors", "/api/providers/catalog?vendor=openai"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s signed-out = %d", path, rec.Code)
		}
	}

	// Vendors route returns the domain vendor table.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/providers/vendors", nil)
	req.Header.Set("X-User", "u1")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("vendors status %d body %s", rec.Code, rec.Body.String())
	}
	var vendors []vendorJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &vendors); err != nil || len(vendors) != len(domain.Vendors()) {
		t.Fatalf("vendors = %+v, err=%v", vendors, err)
	}
	if vendors[0].Key != "anthropic" || vendors[0].Name != "Anthropic" || vendors[0].SuggestedPlan != domain.PlanMetered {
		t.Fatalf("first vendor = %+v", vendors[0])
	}

	// Catalog route returns that vendor's rows.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/providers/catalog?vendor=OpenAI", nil)
	req.Header.Set("X-User", "u1")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status %d body %s", rec.Code, rec.Body.String())
	}
	var rows []catalogRowJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("catalog = %s, err=%v", rec.Body.String(), err)
	}
	if rows[0].Model != "gpt-4o" || rows[0].Kind != "chat" || !rows[0].Tools || *rows[0].InputPerMTok != 2.5 || *rows[0].MaxOutput != 16384 {
		t.Fatalf("catalog row = %+v", rows[0])
	}
}
