package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// ErrUsageForbidden refuses a workspace usage read without usage:view.
var ErrUsageForbidden = errors.New("gateway: workspace usage needs usage:view")

// UsageStore reads the ledger under row security.
type UsageStore interface {
	// PersonalUsage reads the caller's own rows with an empty allow list.
	PersonalUsage(ctx context.Context, who Caller, w domain.UsageWindow) (domain.UsageScan, error)
	// WorkspaceUsage reads every row the caller's allow list admits, grouped
	// by group.
	WorkspaceUsage(ctx context.Context, who Caller, w domain.UsageWindow, group domain.UsageGroup) (domain.UsageScan, error)
	// RecordWorkspaceView seals one workspace usage read.
	RecordWorkspaceView(ctx context.Context, who Caller, r domain.UsageRange, group domain.UsageGroup, format string) error
}

// UsageService serves the usage reports. The callbacks keep the gateway
// independent of identity, agents and tasks.
type UsageService struct {
	Store UsageStore
	// Allow returns the caller's effective permissions.
	Allow func(ctx context.Context, user string) (string, error)
	// Teams returns the caller's teams, so team accounts keep their names.
	Teams func(ctx context.Context, user string) ([]string, error)
	// TimeZone returns the zone the caller's buckets are cut in.
	TimeZone func(ctx context.Context, user string) (string, error)
	// People and TeamNames name people and teams by id.
	People    func(ctx context.Context, ids []string) (map[string]string, error)
	TeamNames func(ctx context.Context, ids []string) (map[string]string, error)
	// Agents names the agents the caller may see.
	Agents func(ctx context.Context, user string, ids []string) (map[string]string, error)
	// Goals names the tasks the caller may see.
	Goals func(ctx context.Context, user string, ids []string) (map[string]string, error)
	Now   func() time.Time
}

func (s UsageService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s UsageService) window(ctx context.Context, user string, r domain.UsageRange) (domain.UsageWindow, error) {
	tz := "UTC"
	if s.TimeZone != nil {
		got, err := s.TimeZone(ctx, user)
		if err != nil {
			return domain.UsageWindow{}, err
		}
		if got != "" {
			tz = got
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return domain.UsageWindow{}, fmt.Errorf("gateway: invalid time zone %q: %w", tz, err)
	}
	return domain.UsageWindowFor(r, s.now(), loc), nil
}

func (s UsageService) teams(ctx context.Context, user string) ([]string, error) {
	if s.Teams == nil {
		return nil, nil
	}
	teams, err := s.Teams(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("gateway: teams: %w", err)
	}
	return teams, nil
}

// Personal returns the caller's own usage. A usage:view holder still sees
// only their own rows here.
func (s UsageService) Personal(ctx context.Context, user string, r domain.UsageRange) (domain.UsageReport, error) {
	if s.Store == nil {
		return domain.UsageReport{}, fmt.Errorf("gateway: no usage store")
	}
	w, err := s.window(ctx, user, r)
	if err != nil {
		return domain.UsageReport{}, err
	}
	teams, err := s.teams(ctx, user)
	if err != nil {
		return domain.UsageReport{}, err
	}
	scan, err := s.Store.PersonalUsage(ctx, Caller{User: user, Teams: teams}, w)
	if err != nil {
		return domain.UsageReport{}, err
	}
	goals := map[string]string{}
	if s.Goals != nil && len(scan.Tasks) > 0 {
		ids := make([]string, 0, len(scan.Tasks))
		for _, t := range scan.Tasks {
			ids = append(ids, t.Task)
		}
		if goals, err = s.Goals(ctx, user, ids); err != nil {
			return domain.UsageReport{}, fmt.Errorf("gateway: task goals: %w", err)
		}
	}
	return domain.PersonalUsage(scan, w, goals), nil
}

// Workspace returns everyone's usage grouped by group, including system
// calls, and seals the read. The caller needs usage:view; row security
// checks it again.
func (s UsageService) Workspace(ctx context.Context, user string, r domain.UsageRange, group domain.UsageGroup, format string) (domain.WorkspaceUsageReport, error) {
	if s.Store == nil {
		return domain.WorkspaceUsageReport{}, fmt.Errorf("gateway: no usage store")
	}
	who := Caller{User: user}
	if s.Allow != nil {
		allow, err := s.Allow(ctx, user)
		if err != nil {
			return domain.WorkspaceUsageReport{}, fmt.Errorf("gateway: permissions: %w", err)
		}
		who.Allow = allow
	}
	if !who.mayViewUsage() {
		return domain.WorkspaceUsageReport{}, ErrUsageForbidden
	}
	w, err := s.window(ctx, user, r)
	if err != nil {
		return domain.WorkspaceUsageReport{}, err
	}
	if who.Teams, err = s.teams(ctx, user); err != nil {
		return domain.WorkspaceUsageReport{}, err
	}
	scan, err := s.Store.WorkspaceUsage(ctx, who, w, group)
	if err != nil {
		return domain.WorkspaceUsageReport{}, err
	}
	names, err := s.names(ctx, user, group, scan.Keyed)
	if err != nil {
		return domain.WorkspaceUsageReport{}, err
	}
	report := domain.WorkspaceUsage(scan, w, group, names)
	if err := s.Store.RecordWorkspaceView(ctx, who, r, group, format); err != nil {
		return domain.WorkspaceUsageReport{}, err
	}
	return report, nil
}

// names looks up the visible names of the keys for the groups named outside
// the gateway.
func (s UsageService) names(ctx context.Context, user string, group domain.UsageGroup, cells []domain.UsageCell) (map[string]string, error) {
	seen := map[string]bool{}
	ids := []string{}
	for _, c := range cells {
		if c.Key != "" && !seen[c.Key] {
			seen[c.Key] = true
			ids = append(ids, c.Key)
		}
	}
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	var (
		names map[string]string
		err   error
	)
	switch {
	case group == domain.GroupPerson && s.People != nil:
		names, err = s.People(ctx, ids)
	case group == domain.GroupTeam && s.TeamNames != nil:
		names, err = s.TeamNames(ctx, ids)
	case group == domain.GroupAgent && s.Agents != nil:
		names, err = s.Agents(ctx, user, ids)
	default:
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gateway: %s names: %w", group, err)
	}
	return names, nil
}
