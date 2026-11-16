package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Route error sentinels surfaced by the HTTP layer with fixed plain messages.
var (
	ErrRouteForbidden        = errors.New("gateway: route cannot be changed")
	ErrRouteShareOnly        = errors.New("gateway: sharing a route needs settings:manage")
	ErrRouteDuplicate        = errors.New("gateway: route name in use")
	ErrRouteAccountForbidden = errors.New("gateway: cannot use provider account in route")
)

// RouteSpec is the validated route payload passed to RouteStore on create/update.
type RouteSpec struct {
	Name       string
	Capability string
	Decision   string
	Visibility string
	TeamIDs    []string
	Hops       []domain.RouteHop
	HopsSet    bool
}

// RouteStore persists gateway routes and their hop chains under row security.
type RouteStore interface {
	List(ctx context.Context, who Caller) ([]ListedRoute, error)
	Get(ctx context.Context, who Caller, id string) (ListedRoute, error)
	Create(ctx context.Context, who Caller, spec RouteSpec) (ListedRoute, error)
	Update(ctx context.Context, who Caller, id string, spec RouteSpec) (ListedRoute, error)
	Share(ctx context.Context, who Caller, id, visibility string, teamIDs []string) (ListedRoute, error)
	Remove(ctx context.Context, who Caller, id string) error
}

// NewRouteHop is one hop in a create or update request. Nil pointer fields
// take defaults (sequential position when all positions are omitted, weight 1,
// retry 1).
type NewRouteHop struct {
	Position             *int
	AccountID            string
	Model                string
	Weight               *int
	Retry                *int
	RequiredCapabilities []string
}

// NewRoute is the input for creating a route.
type NewRoute struct {
	Name       string
	Capability string
	Decision   string
	Visibility string
	TeamIDs    []string
	Hops       []NewRouteHop
}

// RouteChange is the input for updating a route. Nil fields are left as stored.
type RouteChange struct {
	Name       *string
	Capability *string
	Decision   *string
	Visibility *string
	TeamIDs    *[]string
	Hops       *[]NewRouteHop
}

// RouteService is the use case behind creating, editing, sharing, and deleting
// gateway routes.
type RouteService struct {
	Store RouteStore
	Allow func(ctx context.Context, user string) (string, error)
	Teams func(ctx context.Context, user string) ([]string, error)
	Names func(ctx context.Context, teams []string) (map[string]string, error)
	Log   *slog.Logger
}

func (s RouteService) caller(ctx context.Context, user string) (Caller, error) {
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

func (s RouteService) knownTeams(ctx context.Context, teams []string) error {
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

func (c Caller) managesRoute(r ListedRoute) bool {
	if r.Installed {
		return false
	}
	if routeVisibility(r) == domain.VisibilityPrivate {
		return c.User != "" && r.OwnerUserID == c.User
	}
	return c.mayShare()
}

func (c Caller) usesRoute(r ListedRoute) bool {
	if r.Installed {
		return true
	}
	switch routeVisibility(r) {
	case domain.VisibilityPrivate:
		return c.User != "" && r.OwnerUserID == c.User
	case domain.VisibilityEveryone:
		return true
	case domain.VisibilityTeam:
		for _, id := range r.TeamIDs {
			if slices.Contains(c.Teams, id) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func routeVisibility(r ListedRoute) string {
	if r.Visibility != "" {
		return r.Visibility
	}
	if r.Installed {
		return domain.VisibilityEveryone
	}
	return ""
}

func (s RouteService) note(r *ListedRoute, who Caller) {
	r.CanManage = who.managesRoute(*r)
	r.CanUse = who.usesRoute(*r)
	r.IsOwner = !r.Installed && who.User != "" && r.OwnerUserID == who.User
	if r.TeamIDs == nil {
		r.TeamIDs = []string{}
	}
	if r.TeamNames == nil {
		r.TeamNames = []string{}
	}
	if r.Hops == nil {
		r.Hops = []ListedHop{}
	}
}

func (s RouteService) manageable(ctx context.Context, who Caller, id string) (ListedRoute, error) {
	cur, err := s.Store.Get(ctx, who, id)
	if err != nil {
		return ListedRoute{}, err
	}
	if !who.managesRoute(cur) {
		return ListedRoute{}, ErrRouteForbidden
	}
	return cur, nil
}

// List returns the routes visible to user, annotated with can_manage, can_use,
// and is_owner.
func (s RouteService) List(ctx context.Context, user string) ([]ListedRoute, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.List(ctx, who)
	if err != nil {
		return nil, fmt.Errorf("gateway: list routes: %w", err)
	}
	if rows == nil {
		rows = []ListedRoute{}
	}
	for i := range rows {
		s.note(&rows[i], who)
	}
	return rows, nil
}

// Create validates and creates a new route owned by user.
func (s RouteService) Create(ctx context.Context, user string, in NewRoute) (ListedRoute, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return ListedRoute{}, err
	}
	plain := (in.Visibility == "" || in.Visibility == domain.VisibilityPrivate) && len(in.TeamIDs) == 0
	if !plain && !who.mayShare() {
		return ListedRoute{}, ErrRouteShareOnly
	}
	visibility, teams, err := domain.CleanRouteVisibility(in.Visibility, in.TeamIDs)
	if err != nil {
		return ListedRoute{}, err
	}
	if err := s.knownTeams(ctx, teams); err != nil {
		return ListedRoute{}, err
	}
	name, err := domain.CleanRouteName(in.Name)
	if err != nil {
		return ListedRoute{}, err
	}
	cap, dec, err := domain.CleanRouteCapability(in.Capability, in.Decision)
	if err != nil {
		return ListedRoute{}, err
	}
	rawHops, err := normalizeNewHops(in.Hops)
	if err != nil {
		return ListedRoute{}, err
	}
	hops, err := domain.CleanRouteHops(rawHops)
	if err != nil {
		return ListedRoute{}, err
	}
	made, err := s.Store.Create(ctx, who, RouteSpec{
		Name:       name,
		Capability: string(cap),
		Decision:   string(dec),
		Visibility: visibility,
		TeamIDs:    teams,
		Hops:       hops,
		HopsSet:    true,
	})
	if err != nil {
		return ListedRoute{}, err
	}
	s.note(&made, who)
	if s.Log != nil {
		s.Log.Info("route created", "route", made.ID, "owner", user)
	}
	return made, nil
}

// Update validates and applies changes to an existing non-installed route.
func (s RouteService) Update(ctx context.Context, user, id string, in RouteChange) (ListedRoute, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return ListedRoute{}, err
	}
	cur, err := s.manageable(ctx, who, id)
	if err != nil {
		return ListedRoute{}, err
	}
	nextName := cur.Name
	if in.Name != nil {
		if nextName, err = domain.CleanRouteName(*in.Name); err != nil {
			return ListedRoute{}, err
		}
	}
	nextCap := cur.Capability
	nextDec := cur.Decision
	if in.Capability != nil || in.Decision != nil {
		if in.Capability != nil {
			nextCap = *in.Capability
			if *in.Capability != string(domain.CapDecide) && in.Decision == nil {
				nextDec = ""
			}
		}
		if in.Decision != nil {
			nextDec = *in.Decision
		}
		c, d, err := domain.CleanRouteCapability(nextCap, nextDec)
		if err != nil {
			return ListedRoute{}, err
		}
		nextCap, nextDec = string(c), string(d)
	}
	nextVis, nextTeams := routeVisibility(cur), cur.TeamIDs
	if in.Visibility != nil || in.TeamIDs != nil {
		ask := nextVis
		if in.Visibility != nil {
			ask = *in.Visibility
		}
		ids := nextTeams
		switch {
		case in.TeamIDs != nil:
			ids = *in.TeamIDs
		case ask != domain.VisibilityTeam:
			ids = nil
		}
		vis, teams, err := domain.CleanRouteVisibility(ask, ids)
		if err != nil {
			return ListedRoute{}, err
		}
		if vis != domain.VisibilityPrivate && !who.mayShare() {
			return ListedRoute{}, ErrRouteShareOnly
		}
		if vis == domain.VisibilityPrivate && routeVisibility(cur) != domain.VisibilityPrivate && cur.OwnerUserID != who.User {
			return ListedRoute{}, domain.Invalid("Only the owner can make a shared route private.")
		}
		if err := s.knownTeams(ctx, teams); err != nil {
			return ListedRoute{}, err
		}
		nextVis, nextTeams = vis, teams
	}
	var hops []domain.RouteHop
	hopsSet := false
	if in.Hops != nil {
		rawHops, err := normalizeNewHops(*in.Hops)
		if err != nil {
			return ListedRoute{}, err
		}
		if hops, err = domain.CleanRouteHops(rawHops); err != nil {
			return ListedRoute{}, err
		}
		hopsSet = true
	}
	made, err := s.Store.Update(ctx, who, id, RouteSpec{
		Name:       nextName,
		Capability: nextCap,
		Decision:   nextDec,
		Visibility: nextVis,
		TeamIDs:    nextTeams,
		Hops:       hops,
		HopsSet:    hopsSet,
	})
	if err != nil {
		return ListedRoute{}, err
	}
	s.note(&made, who)
	if s.Log != nil {
		s.Log.Info("route updated", "route", made.ID, "owner", user)
	}
	return made, nil
}

// Share changes a non-installed route's visibility and teams. Requires settings:manage.
func (s RouteService) Share(ctx context.Context, user, id, visibility string, teamIDs []string) (ListedRoute, error) {
	who, err := s.caller(ctx, user)
	if err != nil {
		return ListedRoute{}, err
	}
	if !who.mayShare() {
		return ListedRoute{}, ErrRouteShareOnly
	}
	cur, err := s.manageable(ctx, who, id)
	if err != nil {
		return ListedRoute{}, err
	}
	if visibility == "" {
		return ListedRoute{}, domain.Invalid("Choose private, team or everyone.")
	}
	vis, teams, err := domain.CleanRouteVisibility(visibility, teamIDs)
	if err != nil {
		return ListedRoute{}, err
	}
	if vis == domain.VisibilityPrivate && routeVisibility(cur) != domain.VisibilityPrivate && cur.OwnerUserID != who.User {
		return ListedRoute{}, domain.Invalid("Only the owner can make a shared route private.")
	}
	if err := s.knownTeams(ctx, teams); err != nil {
		return ListedRoute{}, err
	}
	made, err := s.Store.Share(ctx, who, id, vis, teams)
	if err != nil {
		return ListedRoute{}, err
	}
	s.note(&made, who)
	if s.Log != nil {
		s.Log.Info("route shared", "route", made.ID, "owner", user)
	}
	return made, nil
}

// Remove deletes a non-installed route owned or managed by user.
func (s RouteService) Remove(ctx context.Context, user, id string) error {
	who, err := s.caller(ctx, user)
	if err != nil {
		return err
	}
	if _, err := s.manageable(ctx, who, id); err != nil {
		return err
	}
	if err := s.Store.Remove(ctx, who, id); err != nil {
		return err
	}
	if s.Log != nil {
		s.Log.Info("route removed", "route", id, "owner", user)
	}
	return nil
}

func normalizeNewHops(in []NewRouteHop) ([]domain.RouteHop, error) {
	if len(in) == 0 {
		return nil, nil
	}
	hasPos := false
	missingPos := false
	for _, h := range in {
		if h.Position != nil {
			hasPos = true
		} else {
			missingPos = true
		}
	}
	if hasPos && missingPos {
		return nil, domain.Invalid("Give every hop a position, or omit positions on all hops.")
	}
	out := make([]domain.RouteHop, 0, len(in))
	for i, h := range in {
		pos := i + 1
		if h.Position != nil {
			pos = *h.Position
		}
		weight := 1
		if h.Weight != nil {
			weight = *h.Weight
		}
		retry := 1
		if h.Retry != nil {
			retry = *h.Retry
		}
		out = append(out, domain.RouteHop{
			Position:             pos,
			AccountID:            h.AccountID,
			Model:                h.Model,
			Weight:               weight,
			Retry:                retry,
			RequiredCapabilities: h.RequiredCapabilities,
		})
	}
	return out, nil
}
