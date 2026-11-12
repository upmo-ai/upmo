package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	// ErrDuplicate is a name the person already uses.
	ErrDuplicate = errors.New("gateway: provider name in use")
	// ErrLimit is a person at the cap of own accounts.
	ErrLimit = errors.New("gateway: too many providers")
	// ErrRate is a person who tested too often.
	ErrRate = errors.New("gateway: too many tests")
	// ErrLocalPlan is a local-plan account, which reaches private addresses
	// and so only the people who manage the workspace may add or test.
	ErrLocalPlan = errors.New("gateway: local provider plan")
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
	User  string
	Allow string
	Teams []string
}

// mayShare reports whether Allow holds settings:manage.
func (c Caller) mayShare() bool {
	for _, name := range strings.Split(c.Allow, ",") {
		if strings.TrimSpace(name) == domain.SharePermission {
			return true
		}
	}
	return false
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
}

// Prober makes one cheap call through an adapter to see whether an account
// works. It returns the failure class of that call.
type Prober interface {
	Probe(ctx context.Context, attempt Attempt) (domain.FailureClass, error)
}

// Providers is the use case behind a person's provider settings.
type Providers struct {
	Accounts Accounts
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
type TestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
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
		Share:         domain.Share{Visibility: visibility, TeamIDs: teams},
	}, sealed)
	if err != nil {
		return domain.Provider{}, err
	}
	s.note(&made, who)
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
	var sealed *domain.Sealed
	if key != "" {
		boxed, err := s.Keys.Seal(cur.CredentialRef, key)
		if err != nil {
			return domain.Provider{}, err
		}
		sealed = &boxed
		reset = true
	}
	made, err := s.Accounts.Update(ctx, who, next, sealed, reset)
	if err != nil {
		return domain.Provider{}, err
	}
	s.note(&made, who)
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
	if _, err := s.manageable(ctx, who, id); err != nil {
		return err
	}
	if err := s.Accounts.Remove(ctx, who, id); err != nil {
		return err
	}
	s.log("provider removed", id, user)
	return nil
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
	class, _ := probe.Probe(callCtx, Attempt{Adapter: adapter, BaseURL: base, Model: in.Model, Secret: key, PlanKind: plan})
	result := TestResult{OK: class == domain.ClassOK, Error: ProbeMessage(class)}
	if err := s.sealTest(ctx, in.ID, result.OK); err != nil {
		return TestResult{}, err
	}
	if in.ID != "" {
		if err := s.Accounts.Note(ctx, who, in.ID, ProbeStatus(class)); err != nil && s.Log != nil {
			s.Log.Warn("provider status not stored", "account", in.ID, "owner", user)
		}
	}
	return result, nil
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
	case domain.ClassTransient, domain.ClassTimeout, domain.ClassOverloaded, domain.ClassCancelled:
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
