package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Why a provider request was refused. Each has a fixed plain message in the
// HTTP layer; none carries a key.
var (
	// ErrNotFound is an account that does not exist or belongs to someone else.
	ErrNotFound = errors.New("gateway: no such provider")
	// ErrGlobal is a workspace account, or a shared one the person may not
	// change, which these routes never change for them.
	ErrGlobal = errors.New("gateway: provider is global")
	// ErrShareOnly is a person who asked to share an account but does not
	// hold settings:manage.
	ErrShareOnly = errors.New("gateway: sharing a provider needs settings:manage")
	// ErrOverridePermission is a person who tried to change a price override
	// without holding settings:manage.
	ErrOverridePermission = errors.New("gateway: price overrides require settings:manage")
	ErrPricingMapLimit    = errors.New("gateway: pricing map exceeds its size limit")
	// ErrDuplicate is a name the person already uses.
	ErrDuplicate = errors.New("gateway: provider name in use")
	// ErrLimit is a person at the cap of own accounts.
	ErrLimit = errors.New("gateway: too many providers")
	// ErrRate is a person who tested too often.
	ErrRate = errors.New("gateway: too many tests")
	// ErrLocalPlan is a local-plan account, which reaches private addresses
	// and so only the people who manage the workspace may add or test.
	ErrLocalPlan = errors.New("gateway: local provider plan")
	// ErrMemberProvidersDisabled is the workspace policy which lets only
	// settings managers change private provider accounts.
	ErrMemberProvidersDisabled = errors.New("gateway: member providers disabled")
	// ErrNoMasterKey is a server that has no master key, so it cannot hold keys.
	ErrNoMasterKey = domain.ErrNoMasterKey
)

// InUseError is an account some routes still link to.
type InUseError struct{ Routes []string }

func (InUseError) Error() string { return "gateway: provider in use by a route" }

// Caller is the person a provider call is for. Allow is the permissions they
// hold (settings:manage when they may share). Teams are the teams they are
// on. The store writes user, allow and teams into the transaction that row
// security reads.
type Caller struct {
	User                   string
	Allow                  string
	Teams                  []string
	MemberProvidersAllowed bool
}

func (c Caller) hasAllow(perm string) bool {
	for _, name := range strings.Split(c.Allow, ",") {
		if strings.TrimSpace(name) == perm {
			return true
		}
	}
	return false
}

// mayShare reports whether Allow holds settings:manage.
func (c Caller) mayShare() bool {
	return c.hasAllow(domain.SharePermission)
}

// mayViewUsage reports whether Allow holds usage:view.
func (c Caller) mayViewUsage() bool {
	return c.hasAllow(domain.UsageViewPermission)
}

// manages reports whether the caller may change or remove p: a private
// account of their own, or a shared one when they hold settings:manage. A
// house account is read from the environment and nobody changes it here.
func (c Caller) manages(p domain.Provider) bool {
	if p.Share.House {
		return false
	}
	if visibilityOf(p) == domain.VisibilityPrivate {
		return p.Own || (c.User != "" && p.OwnerUserID == c.User)
	}
	return c.mayShare()
}

// uses reports whether the caller may route a call through p. A private
// account is its owner's. A team account is for the teams it names. Everyone,
// and a house account, are for every person.
func (c Caller) uses(p domain.Provider) bool {
	if p.Share.House {
		return true
	}
	switch visibilityOf(p) {
	case domain.VisibilityPrivate:
		return p.Own || (c.User != "" && p.OwnerUserID == c.User)
	case domain.VisibilityEveryone:
		return true
	case domain.VisibilityTeam:
		for _, id := range p.Share.TeamIDs {
			if slices.Contains(c.Teams, id) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func visibilityOf(p domain.Provider) string {
	if p.Share.Visibility != "" {
		return p.Share.Visibility
	}
	if p.Share.House {
		return domain.VisibilityEveryone
	}
	if p.Own {
		return domain.VisibilityPrivate
	}
	return ""
}

// Accounts is where provider accounts and their sealed keys are kept. Every
// call is for one person; an implementation shows them their own accounts,
// the workspace's, and the ones shared with a team of theirs (an admin sees
// every shared one), and changes only the ones they may.
type Accounts interface {
	// List returns own accounts first, then team ones, then workspace ones,
	// each by name.
	List(ctx context.Context, who Caller) ([]domain.Provider, error)
	// Get returns an account the person can see, or ErrNotFound.
	Get(ctx context.Context, who Caller, id string) (domain.Provider, error)
	// Create stores an account with its models and, when sealed is set, its
	// key, in one transaction. Visibility private is the person's own. Team
	// is shared with Share.TeamIDs. Everyone is shared with the workspace.
	// It returns ErrDuplicate or ErrLimit.
	Create(ctx context.Context, who Caller, p domain.Provider, sealed *domain.Sealed) (domain.Provider, error)
	// Update writes the account's fields, its visibility and its teams, and
	// replaces its models. When sealed is set it replaces the key. When
	// reset is set the status goes back to Not connected. It returns
	// ErrNotFound, ErrDuplicate or ErrLimit.
	Update(ctx context.Context, who Caller, p domain.Provider, sealed *domain.Sealed, reset bool) (domain.Provider, error)
	// Remove deletes the account, its models and its key in one transaction.
	// It returns ErrNotFound, or InUseError while a route links to it.
	Remove(ctx context.Context, who Caller, id string) error
	// Sealed returns the stored key for an account's ref, or ErrNotFound.
	Sealed(ctx context.Context, who Caller, ref string) (domain.Sealed, error)
	// Note stores the status a test found for an account.
	Note(ctx context.Context, who Caller, id, status string) error
	// PutSeenModels replaces the discovered model list stored for an account.
	PutSeenModels(ctx context.Context, who Caller, id string, models []domain.SeenModel) error
	// SeenModels returns the discovered models stored for an account the person
	// can see, or ErrNotFound.
	SeenModels(ctx context.Context, who Caller, id string) ([]domain.SeenModel, error)
}

// RouteUses is the optional confirmation port implemented by the provider
// store. Keeping it separate avoids expanding every lightweight Accounts fake.
type RouteUses interface {
	RouteUses(ctx context.Context, who Caller, id string) ([]string, error)
}

// EndpointSeenModels is an optional extension of Accounts that persists
// discovered models only while the account's saved adapter and base URL still
// match the probed endpoint.
type EndpointSeenModels interface {
	PutSeenModelsForEndpoint(ctx context.Context, who Caller, id, adapter, baseURL string, models []domain.SeenModel) error
}

// OverrideStore is the optional price-override port implemented by the provider
// store.
type OverrideStore interface {
	SeenModel(ctx context.Context, who Caller, accountID, model string) (*domain.SeenModel, error)
	GetOverride(ctx context.Context, who Caller, accountID, model string) (*domain.PriceOverride, error)
	SetOverride(ctx context.Context, who Caller, accountID string, override domain.PriceOverride) (domain.PriceOverride, error)
	DeleteOverride(ctx context.Context, who Caller, accountID, model string) error
}

type PricingMapStore interface {
	SetPricingMap(ctx context.Context, who Caller, accountID string, vendor *string, models map[string]string) error
}

// ModelPricingView is the merged pricing and override state for one account
// model, together with any >20% divergence warnings.
type ModelPricingView struct {
	Merged   domain.MergedModel       `json:"merged"`
	Override *domain.PriceOverride    `json:"override,omitempty"`
	Warnings []domain.OverrideWarning `json:"warnings"`
}

// ProviderUses is the confirmation information shown before an account is
// disabled or removed.
type ProviderUses struct {
	Routes      []string `json:"routes"`
	Agents      []string `json:"agents"`
	Automations []string `json:"automations"`
}

// Prober makes one cheap call through an adapter to see whether an account
// works, and returns the models the provider lists alongside the failure class.
// A malformed or oversized model list returns nil while keeping ClassOK when
// the auth call succeeded.
type Prober interface {
	Probe(ctx context.Context, attempt Attempt) ([]domain.SeenModel, domain.FailureClass, error)
}

// Providers is the use case behind a person's provider settings.
type Providers struct {
	Accounts Accounts
	// Catalog reads model kinds and vendor catalog rows from gateway.prices.
	Catalog CatalogReader
	// Keys seals and opens the keys a person adds.
	Keys domain.Keyring
	// Known reports whether the gateway has an adapter by that key.
	Known func(adapter string) bool
	// Probes holds the test call of each adapter that has one.
	Probes map[string]Prober
	// Limiter bounds how often a person may test. Nil allows every test.
	Limiter *TestLimiter
	// LocalPlan says whether this person may add or test a local-plan
	// account. Nil denies the local plan to every person.
	LocalPlan func(ctx context.Context, user string) bool
	// Allow is settings:manage when the person holds it, otherwise empty.
	// Nil means nobody may share.
	Allow func(ctx context.Context, user string) (string, error)
	// Teams lists the teams the person is on. Nil means they are on none.
	Teams func(ctx context.Context, user string) ([]string, error)
	// MemberProvidersAllowed says whether members may use and change their own
	// private providers. Nil preserves the default-on workspace setting.
	MemberProvidersAllowed func(ctx context.Context, user string) (bool, error)
	// AgentUses and AutomationUses are ports wired by brain. They avoid a
	// gateway dependency on either module's adapters. Each gets the account's
	// provider and models, and the names of the routes linked to it, and
	// returns the names of what the person may see that depends on it.
	AgentUses      func(ctx context.Context, who Caller, provider string, models, routes []string) ([]string, error)
	AutomationUses func(ctx context.Context, who Caller, provider string, models, routes []string) ([]string, error)
	// Names returns the names of the teams that exist. A missing id is unknown.
	// Nil leaves that check to the store.
	Names func(ctx context.Context, teams []string) (map[string]string, error)
	// SealTest records one test's outcome in a transaction of its own. The
	// store sets it: a test has no business transaction to record inside.
	// Nil leaves the test unrecorded, which the route's audit middleware
	// surfaces.
	SealTest func(ctx context.Context, id string, ok bool) error
	// Log records add, update and remove with ids only. Nil logs nothing.
	Log *slog.Logger
}

// TestTimeout bounds one test call.
const TestTimeout = 10 * time.Second

// NewProvider is what a person sends to add an account. Visibility private,
// or none, is a personal account. Team shares it with TeamIDs. Everyone
// shares it with the workspace.
type NewProvider struct {
	Provider   string
	Name       string
	Adapter    string
	PlanKind   string
	BaseURL    string
	APIKey     string
	Models     []string
	Visibility string
	TeamIDs    []string
}

// ProviderChange is what a person sends to change an account. A nil field is
// left as it is. An empty APIKey leaves the saved key as it is. Visibility
// and TeamIDs change who the account is for.
type ProviderChange struct {
	Name       *string
	PlanKind   *string
	BaseURL    *string
	APIKey     *string
	Models     *[]string
	Visibility *string
	TeamIDs    *[]string
	Enabled    *bool
}

// TestRequest is what a person sends to test an account. With an ID and no
// APIKey the saved key is used.
type TestRequest struct {
	ID       string
	Provider string
	Adapter  string
	PlanKind string
	APIKey   string
	BaseURL  string
	Model    string
}

// TestResult is the answer to a test. Error is a fixed plain message.
// Models carries the discovered models when the auth call worked and the list
// parsed cleanly.
type TestResult struct {
	OK     bool               `json:"ok"`
	Error  string             `json:"error,omitempty"`
	Models []domain.SeenModel `json:"models,omitempty"`
}

// caller names the person, the permissions they hold, and the teams they are on.
func (s Providers) caller(ctx context.Context, user string) (Caller, error) {
	who := Caller{User: user}
	if s.Allow != nil {
		allow, err := s.Allow(ctx, user)
		if err != nil {
			return Caller{}, fmt.Errorf("gateway: sharing permission: %w", err)
		}
		who.Allow = allow
	}
	if s.Teams != nil {
		teams, err := s.Teams(ctx, user)
		if err != nil {
			return Caller{}, fmt.Errorf("gateway: teams: %w", err)
		}
		who.Teams = teams
	}
	who.MemberProvidersAllowed = true
	if s.MemberProvidersAllowed != nil {
		allowed, err := s.MemberProvidersAllowed(ctx, user)
		if err != nil {
			return Caller{}, fmt.Errorf("gateway: member providers: %w", err)
		}
		who.MemberProvidersAllowed = allowed || who.mayShare()
	}
	return who, nil
}

// CanShare reports whether the person may share accounts.
func (s Providers) CanShare(ctx context.Context, user string) (bool, error) {
	who, err := s.caller(ctx, user)
	return who.mayShare(), err
}

// List returns the accounts the person may see: their own, the ones shared
// with a team of theirs, and the workspace's. Each says whether they may
// change it.
func (s Providers) List(ctx context.Context, user string) ([]domain.Provider, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return nil, err
	}
	out, err := s.Accounts.List(ctx, who)
	if err != nil {
		return nil, fmt.Errorf("gateway: list providers: %w", err)
	}
	for i := range out {
		s.note(&out[i], who)
	}
	if err := s.annotateKinds(ctx, out); err != nil {
		return nil, fmt.Errorf("gateway: list providers catalog: %w", err)
	}
	return out, nil
}

// Add validates and stores a new account: the person's own, or a shared one
// when the request names a shared visibility and the person holds settings:manage.
func (s Providers) Add(ctx context.Context, user string, in NewProvider) (domain.Provider, error) {
	if !s.Keys.Ready() {
		return domain.Provider{}, ErrNoMasterKey
	}
	who, err := s.caller(ctx, user)
	if err != nil {
		return domain.Provider{}, err
	}
	plain := (in.Visibility == "" || in.Visibility == domain.VisibilityPrivate) && len(in.TeamIDs) == 0
	if plain && !who.MemberProvidersAllowed {
		return domain.Provider{}, ErrMemberProvidersDisabled
	}
	if !plain && !who.mayShare() {
		return domain.Provider{}, ErrShareOnly
	}
	visibility, teams, err := domain.CleanVisibility(in.Visibility, in.TeamIDs)
	if err != nil {
		return domain.Provider{}, err
	}
	if err := s.knownTeams(ctx, teams); err != nil {
		return domain.Provider{}, err
	}
	label, err := domain.CleanLabel(in.Provider)
	if err != nil {
		return domain.Provider{}, err
	}
	name, err := domain.CleanName(in.Name)
	if err != nil {
		return domain.Provider{}, err
	}
	if s.Known == nil || !s.Known(in.Adapter) {
		return domain.Provider{}, domain.Invalid("That adapter is not available.")
	}
	plan, err := domain.CleanPlan(in.PlanKind)
	if err != nil {
		return domain.Provider{}, err
	}
	if plan == domain.PlanLocal && !s.mayLocal(ctx, user) {
		return domain.Provider{}, ErrLocalPlan
	}
	base, err := domain.CleanBaseURL(plan, in.BaseURL)
	if err != nil {
		return domain.Provider{}, err
	}
	key, err := domain.CleanKey(in.APIKey)
	if err != nil {
		return domain.Provider{}, err
	}
	if err := domain.NeedKey(plan, key, false); err != nil {
		return domain.Provider{}, err
	}
	models, err := domain.CleanModels(in.Models)
	if err != nil {
		return domain.Provider{}, err
	}
	if err := domain.CheckScripted(in.Adapter, models); err != nil {
		return domain.Provider{}, err
	}
	ref, err := newRef()
	if err != nil {
		return domain.Provider{}, err
	}
	var sealed *domain.Sealed
	if key != "" {
		boxed, err := s.Keys.Seal(ref, key)
		if err != nil {
			return domain.Provider{}, err
		}
		sealed = &boxed
	}
	made, err := s.Accounts.Create(ctx, who, domain.Provider{
		Provider:      label,
		Name:          name,
		Adapter:       in.Adapter,
		PlanKind:      plan,
		BaseURL:       base,
		Status:        domain.StatusNotConnected,
		Models:        models,
		Own:           visibility == domain.VisibilityPrivate,
		CredentialRef: ref,
		KeyHint:       domain.KeyHint(key),
		Share:         domain.Share{Visibility: visibility, TeamIDs: teams},
	}, sealed)
	if err != nil {
		return domain.Provider{}, err
	}
	s.note(&made, who)
	if err := s.annotateOneKind(ctx, &made); err != nil {
		return domain.Provider{}, err
	}
	s.log("provider added", made.ID, user)
	return made, nil
}

// Change validates and stores a change to one account the person may change.
func (s Providers) Change(ctx context.Context, user, id string, in ProviderChange) (domain.Provider, error) {
	if !s.Keys.Ready() {
		return domain.Provider{}, ErrNoMasterKey
	}
	who, err := s.caller(ctx, user)
	if err != nil {
		return domain.Provider{}, err
	}
	cur, err := s.manageable(ctx, who, id)
	if err != nil {
		return domain.Provider{}, err
	}
	if visibilityOf(cur) == domain.VisibilityPrivate && !who.MemberProvidersAllowed {
		return domain.Provider{}, ErrMemberProvidersDisabled
	}
	next := cur
	reset := false
	if in.Visibility != nil || in.TeamIDs != nil {
		ask := visibilityOf(cur)
		if in.Visibility != nil {
			ask = *in.Visibility
		}
		ids := cur.Share.TeamIDs
		switch {
		case in.TeamIDs != nil:
			ids = *in.TeamIDs
		case ask != domain.VisibilityTeam:
			ids = nil
		}
		vis, teams, err := domain.CleanVisibility(ask, ids)
		if err != nil {
			return domain.Provider{}, err
		}
		if vis != domain.VisibilityPrivate && !who.mayShare() {
			return domain.Provider{}, ErrShareOnly
		}
		// Taking a shared account private is the owner's act. settings:manage
		// can change who a shared account is for, and cannot take someone
		// else's private.
		if vis == domain.VisibilityPrivate && visibilityOf(cur) != domain.VisibilityPrivate && cur.OwnerUserID != who.User {
			return domain.Provider{}, domain.Invalid("Only the owner can make a shared provider private.")
		}
		if err := s.knownTeams(ctx, teams); err != nil {
			return domain.Provider{}, err
		}
		next.Share.Visibility, next.Share.TeamIDs = vis, teams
		next.Own = vis == domain.VisibilityPrivate
	}
	if in.Name != nil {
		if next.Name, err = domain.CleanName(*in.Name); err != nil {
			return domain.Provider{}, err
		}
	}
	if in.PlanKind != nil {
		if next.PlanKind, err = domain.CleanPlan(*in.PlanKind); err != nil {
			return domain.Provider{}, err
		}
		reset = reset || next.PlanKind != cur.PlanKind
	}
	// Making an account local, or re-pointing one that is, is the act Add
	// guards: it reaches private addresses, so the same people decide it.
	if next.PlanKind == domain.PlanLocal && (in.PlanKind != nil || in.BaseURL != nil) {
		if !s.mayLocal(ctx, user) {
			return domain.Provider{}, ErrLocalPlan
		}
	}
	if in.BaseURL != nil {
		next.BaseURL = *in.BaseURL
	}
	// The saved URL is judged again whenever either it or the plan changes.
	if in.BaseURL != nil || in.PlanKind != nil {
		if next.BaseURL, err = domain.CleanBaseURL(next.PlanKind, next.BaseURL); err != nil {
			return domain.Provider{}, err
		}
		reset = reset || next.BaseURL != cur.BaseURL
	}
	var key string
	if in.APIKey != nil {
		if key, err = domain.CleanKey(*in.APIKey); err != nil {
			return domain.Provider{}, err
		}
	}
	if err := domain.NeedKey(next.PlanKind, key, cur.HasKey); err != nil {
		return domain.Provider{}, err
	}
	if in.Models != nil {
		if next.Models, err = domain.CleanModels(*in.Models); err != nil {
			return domain.Provider{}, err
		}
		if err := domain.CheckScripted(next.Adapter, next.Models); err != nil {
			return domain.Provider{}, err
		}
	}
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	var sealed *domain.Sealed
	if key != "" {
		boxed, err := s.Keys.Seal(cur.CredentialRef, key)
		if err != nil {
			return domain.Provider{}, err
		}
		sealed = &boxed
		next.KeyHint = domain.KeyHint(key)
		reset = true
	}
	made, err := s.Accounts.Update(ctx, who, next, sealed, reset)
	if err != nil {
		return domain.Provider{}, err
	}
	s.note(&made, who)
	if err := s.annotateOneKind(ctx, &made); err != nil {
		return domain.Provider{}, err
	}
	s.log("provider updated", made.ID, user)
	return made, nil
}

// Remove deletes one account the person may change, that no route uses.
func (s Providers) Remove(ctx context.Context, user, id string) error {
	if !s.Keys.Ready() {
		return ErrNoMasterKey
	}
	who, err := s.caller(ctx, user)
	if err != nil {
		return err
	}
	cur, err := s.manageable(ctx, who, id)
	if err != nil {
		return err
	}
	if visibilityOf(cur) == domain.VisibilityPrivate && !who.MemberProvidersAllowed {
		return ErrMemberProvidersDisabled
	}
	if err := s.Accounts.Remove(ctx, who, id); err != nil {
		return err
	}
	s.log("provider removed", id, user)
	return nil
}

// Uses returns the route, agent and automation references to an account's
// models for the confirmation shown before disabling or removing it.
func (s Providers) Uses(ctx context.Context, user, id string) (ProviderUses, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return ProviderUses{}, err
	}
	account, err := s.manageable(ctx, who, id)
	if err != nil {
		return ProviderUses{}, err
	}
	routes := []string{}
	if store, ok := s.Accounts.(RouteUses); ok {
		if routes, err = store.RouteUses(ctx, who, id); err != nil {
			return ProviderUses{}, err
		}
	}
	out := ProviderUses{Routes: routes, Agents: []string{}, Automations: []string{}}
	if s.AgentUses != nil {
		if out.Agents, err = s.AgentUses(ctx, who, account.Provider, account.Models, routes); err != nil {
			return ProviderUses{}, err
		}
	}
	if s.AutomationUses != nil {
		if out.Automations, err = s.AutomationUses(ctx, who, account.Provider, account.Models, routes); err != nil {
			return ProviderUses{}, err
		}
	}
	return out, nil
}

// Test makes one cheap call to the provider and says whether it worked. The
// answer is a fixed message; no text from the provider and no key is in it. A
// test of a saved account stores the status it found.
func (s Providers) Test(ctx context.Context, user string, in TestRequest) (TestResult, error) {
	if s.Limiter != nil && !s.Limiter.Allow(user) {
		return TestResult{}, ErrRate
	}
	who, err := s.caller(ctx, user)
	if err != nil {
		return TestResult{}, err
	}
	adapter, plan, base, key := in.Adapter, in.PlanKind, in.BaseURL, in.APIKey
	var saved domain.Provider
	if in.ID != "" {
		var err error
		if saved, err = s.manageable(ctx, who, in.ID); err != nil {
			return TestResult{}, err
		}
		if adapter == "" {
			adapter = saved.Adapter
		}
		if plan == "" {
			plan = saved.PlanKind
		}
		if base == "" {
			base = saved.BaseURL
		}
	}
	if plan == "" {
		plan = domain.PlanMetered
	}
	refuse := func(text string) (TestResult, error) {
		if err := s.sealTest(ctx, in.ID, false); err != nil {
			return TestResult{}, err
		}
		return TestResult{Error: text}, nil
	}

	if s.Known == nil || !s.Known(adapter) {
		return refuse("That adapter is not available.")
	}
	plan, err = domain.CleanPlan(plan)
	if err != nil {
		return refuse(err.Error())
	}
	if plan == domain.PlanLocal && !s.mayLocal(ctx, user) {
		return TestResult{}, ErrLocalPlan
	}
	if base, err = domain.CleanBaseURL(plan, base); err != nil {
		return refuse(err.Error())
	}
	if base == "" && adapter != domain.AdapterFake {
		return refuse("A base URL is required.")
	}
	if key, err = domain.CleanKey(key); err != nil {
		return refuse(err.Error())
	}
	unsavedKey := key != ""
	if key == "" && saved.HasKey {
		boxed, err := s.Accounts.Sealed(ctx, who, saved.CredentialRef)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return TestResult{}, fmt.Errorf("gateway: read saved key: %w", err)
		}
		if err == nil {
			if !s.Keys.Ready() {
				return TestResult{}, ErrNoMasterKey
			}
			if key, err = s.Keys.Open(saved.CredentialRef, boxed); err != nil {
				return refuse("The saved key could not be read. Enter it again.")
			}
		}
	}
	if err := domain.NeedKey(plan, key, false); err != nil {
		return refuse(err.Error())
	}
	probe := s.Probes[adapter]
	if probe == nil {
		return refuse("That adapter cannot be tested.")
	}

	callCtx, cancel := context.WithTimeout(ctx, TestTimeout)
	defer cancel()
	models, class, _ := probe.Probe(callCtx, Attempt{Adapter: adapter, BaseURL: base, Model: in.Model, Secret: key, PlanKind: plan})
	result := TestResult{OK: class == domain.ClassOK, Error: ProbeMessage(class), Models: models}
	if err := s.sealTest(ctx, in.ID, result.OK); err != nil {
		return TestResult{}, err
	}
	sameEndpoint := in.ID != "" && !unsavedKey && adapter == saved.Adapter && base == saved.BaseURL
	if sameEndpoint {
		if err := s.Accounts.Note(ctx, who, in.ID, ProbeStatus(class)); err != nil && s.Log != nil {
			s.Log.Warn("provider status not stored", "account", in.ID, "owner", user)
		}
		if result.OK && models != nil {
			var perr error
			if ep, ok := s.Accounts.(EndpointSeenModels); ok {
				perr = ep.PutSeenModelsForEndpoint(ctx, who, in.ID, adapter, base, models)
			} else {
				perr = s.Accounts.PutSeenModels(ctx, who, in.ID, models)
			}
			if perr != nil && s.Log != nil {
				s.Log.Warn("provider models not stored", "account", in.ID, "owner", user)
			}
		}
	}
	return result, nil
}

// SeenModels returns the discovered models stored for an account the person
// may see, or ErrNotFound.
func (s Providers) SeenModels(ctx context.Context, user, id string) ([]domain.SeenModel, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return nil, err
	}
	return s.Accounts.SeenModels(ctx, who, id)
}

// VendorCatalog returns the catalog rows stored for vendor. Unknown or empty
// vendors return an empty slice.
func (s Providers) VendorCatalog(ctx context.Context, vendor string) ([]domain.CatalogRow, error) {
	if s.Catalog == nil {
		return []domain.CatalogRow{}, nil
	}
	rows, err := s.Catalog.VendorCatalog(ctx, domain.VendorKey(vendor))
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []domain.CatalogRow{}, nil
	}
	return rows, nil
}

// MergedModel resolves the merged model view for one account model across
// optional override, stored provider discovery (account_models_seen), and the
// models.dev catalog.
func (s Providers) MergedModel(ctx context.Context, user, accountID, model string, override *domain.ModelOverride) (domain.MergedModel, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return domain.MergedModel{}, err
	}
	account, err := s.Accounts.Get(ctx, who, accountID)
	if err != nil {
		return domain.MergedModel{}, err
	}
	seenList, err := s.Accounts.SeenModels(ctx, who, accountID)
	if err != nil {
		return domain.MergedModel{}, err
	}
	var seen *domain.SeenModel
	for i := range seenList {
		if seenList[i].ID == model {
			seen = &seenList[i]
			break
		}
	}
	var catalog *domain.CatalogRow
	if s.Catalog != nil {
		vendor := domain.PricingVendorFor(account.Adapter, account.BaseURL)
		catalogModel := model
		if account.PricingProvider != "" {
			vendor = account.PricingProvider
		}
		if account.PricingModels[model] != "" {
			catalogModel = account.PricingModels[model]
		}
		row, ok, err := s.Catalog.CatalogModel(ctx, vendor, catalogModel)
		if err != nil {
			return domain.MergedModel{}, err
		}
		if ok {
			catalog = &row
		}
	}
	return domain.MergeModel(model, override, seen, catalog), nil
}

// GetModelPricing returns the merged model pricing view, any stored override,
// and any >20% divergence warnings for an account model visible to user.
func (s Providers) GetModelPricing(ctx context.Context, user, accountID, model string) (ModelPricingView, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return ModelPricingView{}, err
	}
	cleanModel, err := domain.CleanModelID(model)
	if err != nil {
		return ModelPricingView{}, err
	}
	account, err := s.Accounts.Get(ctx, who, accountID)
	if err != nil {
		return ModelPricingView{}, err
	}
	var override *domain.PriceOverride
	if store, ok := s.Accounts.(OverrideStore); ok {
		override, err = store.GetOverride(ctx, who, accountID, cleanModel)
		if err != nil {
			return ModelPricingView{}, err
		}
	}
	return s.buildModelPricingView(ctx, who, account, cleanModel, override)
}

// SetPriceOverride validates and stores a per-account, per-model price override.
// Requires settings:manage even on a private account.
func (s Providers) SetPriceOverride(ctx context.Context, user, accountID string, in domain.PriceOverride) (ModelPricingView, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return ModelPricingView{}, err
	}
	if !who.mayShare() {
		return ModelPricingView{}, ErrOverridePermission
	}
	clean, err := domain.CleanPriceOverride(in)
	if err != nil {
		return ModelPricingView{}, err
	}
	store, ok := s.Accounts.(OverrideStore)
	if !ok {
		return ModelPricingView{}, fmt.Errorf("gateway: price overrides are not supported")
	}
	account, err := s.Accounts.Get(ctx, who, accountID)
	if err != nil {
		return ModelPricingView{}, err
	}
	saved, err := store.SetOverride(ctx, who, accountID, clean)
	if err != nil {
		return ModelPricingView{}, err
	}
	return s.buildModelPricingView(ctx, who, account, clean.Model, &saved)
}

// DeletePriceOverride removes a per-account, per-model price override.
// Requires settings:manage even on a private account.
func (s Providers) DeletePriceOverride(ctx context.Context, user, accountID, model string) (ModelPricingView, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return ModelPricingView{}, err
	}
	if !who.mayShare() {
		return ModelPricingView{}, ErrOverridePermission
	}
	cleanModel, err := domain.CleanModelID(model)
	if err != nil {
		return ModelPricingView{}, err
	}
	store, ok := s.Accounts.(OverrideStore)
	if !ok {
		return ModelPricingView{}, fmt.Errorf("gateway: price overrides are not supported")
	}
	account, err := s.Accounts.Get(ctx, who, accountID)
	if err != nil {
		return ModelPricingView{}, err
	}
	if err := store.DeleteOverride(ctx, who, accountID, cleanModel); err != nil {
		return ModelPricingView{}, err
	}
	return s.buildModelPricingView(ctx, who, account, cleanModel, nil)
}

// SetPricingMap changes the catalog mapping for an account. Requires settings:manage.
func (s Providers) SetPricingMap(ctx context.Context, user, accountID string, vendor *string, models map[string]string) error {
	who, err := s.caller(ctx, user)
	if err != nil {
		return err
	}
	if !who.mayShare() {
		return ErrOverridePermission
	}
	encodedModels, err := json.Marshal(models)
	if err != nil {
		return err
	}
	if len(encodedModels) > 65536 {
		return ErrPricingMapLimit
	}
	if vendor != nil {
		cleanVendor := strings.TrimSpace(*vendor)
		vendor = &cleanVendor
	}
	if vendor != nil && *vendor != "" {
		known := false
		for _, v := range domain.Vendors() {
			if v.Key == *vendor {
				known = true
				break
			}
		}
		if !known {
			return domain.Invalid("Choose a pricing provider from the list.")
		}
	}
	clean := make(map[string]string, len(models))
	for model, pricingModel := range models {
		id, e := domain.CleanModelID(model)
		if e != nil {
			return e
		}
		pricingModel = strings.TrimSpace(pricingModel)
		if pricingModel == "" {
			clean[id] = ""
			continue
		}
		mapped, e := domain.CleanModelID(pricingModel)
		if e != nil {
			return e
		}
		clean[id] = mapped
	}
	account, err := s.Accounts.Get(ctx, who, accountID)
	if err != nil {
		return err
	}
	effective := make(map[string]string, len(account.PricingModels)+len(clean))
	for model, mapped := range account.PricingModels {
		effective[model] = mapped
	}
	for model, mapped := range clean {
		if mapped == "" {
			delete(effective, model)
		} else {
			effective[model] = mapped
		}
	}
	mergedSize, err := jsonbTextSize(effective)
	if err != nil {
		return err
	}
	if len(effective) > 2000 || mergedSize > 65536 {
		return ErrPricingMapLimit
	}
	store, ok := s.Accounts.(PricingMapStore)
	if !ok {
		return fmt.Errorf("gateway: pricing maps are not supported")
	}
	return store.SetPricingMap(ctx, who, accountID, vendor, clean)
}

// jsonbTextSize matches jsonb::text's two spaces per key/value pair.
func jsonbTextSize(models map[string]string) (int, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(models); err != nil {
		return 0, err
	}
	jsonbText := encoded.Bytes()
	compactSize := len(jsonbText) - 1 // json.Encoder adds a trailing newline.
	for i := 0; i+6 <= len(jsonbText); i++ {
		if jsonbText[i] != '\\' || string(jsonbText[i+1:i+6]) != "u2028" && string(jsonbText[i+1:i+6]) != "u2029" {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && jsonbText[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			compactSize -= 3
		}
	}
	if len(models) == 0 {
		return compactSize, nil
	}
	return compactSize + 2*len(models) - 1, nil
}

func (s Providers) buildModelPricingView(ctx context.Context, who Caller, account domain.Provider, model string, override *domain.PriceOverride) (ModelPricingView, error) {
	var seen *domain.SeenModel
	if store, ok := s.Accounts.(OverrideStore); ok {
		var err error
		seen, err = store.SeenModel(ctx, who, account.ID, model)
		if err != nil {
			return ModelPricingView{}, err
		}
	}
	var catalog *domain.CatalogRow
	var catPrice *domain.Price
	if s.Catalog != nil {
		vendor := domain.PricingVendorFor(account.Adapter, account.BaseURL)
		catalogModel := model
		if account.PricingProvider != "" {
			vendor = account.PricingProvider
		}
		if account.PricingModels[model] != "" {
			catalogModel = account.PricingModels[model]
		}
		row, ok, err := s.Catalog.CatalogModel(ctx, vendor, catalogModel)
		if err != nil {
			return ModelPricingView{}, err
		}
		if ok {
			catalog = &row
			if row.Vendor != "" {
				catPrice = &row.Price
			}
		}
	}
	var provPrice *domain.Price
	if seen != nil {
		if p, ok := domain.ParseSeenPricingMicroUSD(seen.Pricing); ok {
			provPrice = &p
		}
	}
	return ModelPricingView{
		Merged:   domain.MergeModel(model, override.ToModelOverride(), seen, catalog),
		Override: override,
		Warnings: domain.OverrideWarnings(override, provPrice, catPrice),
	}, nil
}

func (s Providers) annotateOneKind(ctx context.Context, p *domain.Provider) error {
	if s.Catalog == nil || len(p.Models) == 0 {
		return nil
	}
	tmp := []domain.Provider{*p}
	if err := s.annotateKinds(ctx, tmp); err != nil {
		return err
	}
	p.ModelKinds = tmp[0].ModelKinds
	return nil
}

func (s Providers) annotateKinds(ctx context.Context, accounts []domain.Provider) error {
	if s.Catalog == nil || len(accounts) == 0 {
		return nil
	}
	seenPairs := map[domain.VendorModel]bool{}
	var pairs []domain.VendorModel
	for _, a := range accounts {
		vendor := domain.PricingVendorFor(a.Adapter, a.BaseURL)
		for _, m := range a.Models {
			k := domain.VendorModel{Vendor: vendor, Model: m}
			if !seenPairs[k] {
				seenPairs[k] = true
				pairs = append(pairs, k)
			}
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	kinds, err := s.Catalog.ModelKinds(ctx, pairs)
	if err != nil {
		return err
	}
	if len(kinds) == 0 {
		return nil
	}
	for i := 0; i < len(accounts); i++ {
		vendor := domain.PricingVendorFor(accounts[i].Adapter, accounts[i].BaseURL)
		var mk map[string]domain.ModelKind
		for _, m := range accounts[i].Models {
			if k, ok := kinds[domain.VendorModel{Vendor: vendor, Model: m}]; ok {
				if mk == nil {
					mk = make(map[string]domain.ModelKind, len(accounts[i].Models))
				}
				mk[m] = k
			}
		}
		accounts[i].ModelKinds = mk
	}
	return nil
}

// mayLocal asks LocalPlan whether this person may add or test a local-plan
// account. Without a policy nobody may: a missing wiring must not open
// private addresses to everyone.
func (s Providers) mayLocal(ctx context.Context, user string) bool {
	return s.LocalPlan != nil && s.LocalPlan(ctx, user)
}

// sealTest records a finished test through SealTest when it is wired.
func (s Providers) sealTest(ctx context.Context, id string, ok bool) error {
	if s.SealTest == nil {
		return nil
	}
	return s.SealTest(ctx, id, ok)
}

// note records whether this person may change the account and use it, and
// whether they own it. Ownership is for the screen. Access and tiering still
// follow visibility and house.
func (s Providers) note(p *domain.Provider, who Caller) {
	p.Share.CanManage = who.manages(*p)
	p.Share.CanUse = who.uses(*p)
	if visibilityOf(*p) == domain.VisibilityPrivate && !who.MemberProvidersAllowed {
		p.Share.CanManage = false
		p.Share.CanUse = false
		p.Status = domain.StatusWorkspaceOff
	}
	if !p.Share.CanManage {
		p.KeyHint = ""
		p.KeyChangedBy = ""
		p.KeyChangedAt = nil
	}
	p.Share.IsOwner = !p.Share.House && who.User != "" && p.OwnerUserID == who.User
}

// knownTeams refuses a team id Names does not know. The store checks again.
func (s Providers) knownTeams(ctx context.Context, teams []string) error {
	if len(teams) == 0 || s.Names == nil {
		return nil
	}
	names, err := s.Names(ctx, teams)
	if err != nil {
		return fmt.Errorf("gateway: team names: %w", err)
	}
	for _, id := range teams {
		if names[id] == "" {
			return domain.Invalid("Choose teams from the list.")
		}
	}
	return nil
}

// manageable loads an account and refuses one the person may not change: a
// house account, a private account of someone else, or a shared one when
// they do not hold settings:manage.
func (s Providers) manageable(ctx context.Context, who Caller, id string) (domain.Provider, error) {
	cur, err := s.Accounts.Get(ctx, who, id)
	if err != nil {
		return domain.Provider{}, err
	}
	if !who.manages(cur) {
		return domain.Provider{}, ErrGlobal
	}
	return cur, nil
}

func (s Providers) log(msg, id, owner string) {
	if s.Log != nil {
		s.Log.Info(msg, "account", id, "owner", owner)
	}
}

// ProbeMessage is the fixed text for a test that did not work. It is empty
// for a test that did.
func ProbeMessage(class domain.FailureClass) string {
	switch class {
	case domain.ClassOK:
		return ""
	case domain.ClassCredential:
		return "The key was rejected."
	case domain.ClassRateLimit:
		return "Rate limited."
	case domain.ClassTransient, domain.ClassTimeout, domain.ClassOverloaded, domain.ClassCanceled:
		return "Could not reach the provider."
	default:
		return "The provider did not accept the request."
	}
}

// ProbeStatus is the status a test result leaves on an account.
func ProbeStatus(class domain.FailureClass) string {
	switch class {
	case domain.ClassOK:
		return domain.StatusConnected
	case domain.ClassCredential:
		return domain.StatusKeyRejected
	case domain.ClassRateLimit:
		return domain.StatusRateLimited
	default:
		return domain.StatusOffline
	}
}

func newRef() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("gateway: new ref")
	}
	return "own_" + hex.EncodeToString(b[:]), nil
}

// TestLimiter allows each person a fixed number of tests per window.
type TestLimiter struct {
	Max    int
	Window time.Duration
	Now    func() time.Time

	mu    sync.Mutex
	hits  map[string][]time.Time
	swept time.Time
}

// NewTestLimiter allows max tests per person in each window.
func NewTestLimiter(max int, window time.Duration) *TestLimiter {
	return &TestLimiter{Max: max, Window: window, Now: time.Now}
}

// Allow records a test by user and reports whether it is within the limit.
func (l *TestLimiter) Allow(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	// Once a window, forget everyone whose last test is older than the window,
	// so the map does not grow with every person who ever tested.
	if now.Sub(l.swept) >= l.Window {
		for who, times := range l.hits {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= l.Window {
				delete(l.hits, who)
			}
		}
		l.swept = now
	}
	recent := l.hits[user][:0]
	for _, at := range l.hits[user] {
		if now.Sub(at) < l.Window {
			recent = append(recent, at)
		}
	}
	if len(recent) >= l.Max {
		l.hits[user] = recent
		return false
	}
	l.hits[user] = append(recent, now)
	return true
}
