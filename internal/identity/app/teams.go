package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// TeamStore is the team rows and their memberships.
type TeamStore interface {
	ListTeams(ctx context.Context) ([]domain.Team, error)
	CreateTeam(ctx context.Context, name string) (domain.TeamID, error)
	AddTeamMember(ctx context.Context, team domain.TeamID, user domain.UserID) error
	RemoveTeamMember(ctx context.Context, team domain.TeamID, user domain.UserID) error
	MemberActor(ctx context.Context, id domain.UserID) (domain.RoleID, string, bool, []string, error)
}

// Teams creates teams and puts joined people on them.
type Teams struct {
	Store TeamStore
	Now   func() time.Time
	Log   *slog.Logger
}

func (t Teams) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// List returns every team for a signed-in person.
func (t Teams) List(ctx context.Context) ([]domain.Team, error) {
	return t.Store.ListTeams(ctx)
}

// Create stores a team when the actor may manage teams.
func (t Teams) Create(ctx context.Context, session Session, name string) (domain.TeamID, error) {
	if err := t.fresh(session); err != nil {
		return domain.TeamID{}, t.refuse("team create refused", session, err)
	}
	actor, err := t.actor(ctx, session.UserID)
	if err != nil {
		return domain.TeamID{}, t.refuse("team create refused", session, err)
	}
	if err := domain.MayManageTeams(actor); err != nil {
		return domain.TeamID{}, t.refuse("team create refused", session, err)
	}
	clean, err := domain.ParseTeamName(name)
	if err != nil {
		return domain.TeamID{}, t.refuse("team create refused", session, err)
	}
	id, err := t.Store.CreateTeam(ctx, clean)
	if err != nil {
		return domain.TeamID{}, t.refuse("team create refused", session, err)
	}
	t.note("team created", session, id)
	return id, nil
}

// Add puts a joined person on a team.
func (t Teams) Add(ctx context.Context, session Session, team domain.TeamID, user domain.UserID) error {
	if err := t.gate(ctx, session, "team member refused"); err != nil {
		return err
	}
	if err := t.Store.AddTeamMember(ctx, team, user); err != nil {
		return t.refuse("team member refused", session, err)
	}
	t.note("team member added", session, team)
	return nil
}

// Remove takes a person off a team.
func (t Teams) Remove(ctx context.Context, session Session, team domain.TeamID, user domain.UserID) error {
	if err := t.gate(ctx, session, "team member refused"); err != nil {
		return err
	}
	if err := t.Store.RemoveTeamMember(ctx, team, user); err != nil {
		return t.refuse("team member refused", session, err)
	}
	t.note("team member removed", session, team)
	return nil
}

func (t Teams) gate(ctx context.Context, session Session, message string) error {
	if err := t.fresh(session); err != nil {
		return t.refuse(message, session, err)
	}
	actor, err := t.actor(ctx, session.UserID)
	if err != nil {
		return t.refuse(message, session, err)
	}
	if err := domain.MayManageTeams(actor); err != nil {
		return t.refuse(message, session, err)
	}
	return nil
}

func (t Teams) actor(ctx context.Context, id domain.UserID) (domain.Subject, error) {
	roleID, system, joined, raw, err := t.Store.MemberActor(ctx, id)
	if err != nil {
		return domain.Subject{}, err
	}
	if !joined {
		return domain.Subject{}, domain.ErrNoSuchMember
	}
	permissions, err := permissionsFor(system, raw)
	if err != nil {
		return domain.Subject{}, err
	}
	return domain.Subject{
		Kind: domain.KindUser, UserID: id, RoleID: roleID, Joined: true, Permissions: permissions,
	}, nil
}

func (t Teams) fresh(session Session) error {
	if !domain.Fresh(session.SignedIn, t.now()) {
		return ErrStale
	}
	return nil
}

func (t Teams) refuse(message string, session Session, err error) error {
	if t.Log != nil && err != nil {
		t.Log.Info(message, "actor", session.UserID.String(), "err", err.Error())
	}
	return err
}

func (t Teams) note(message string, session Session, team domain.TeamID) {
	if t.Log == nil {
		return
	}
	t.Log.Info(message, "actor", session.UserID.String(), "team", team.String())
}
