package app

import (
	"context"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

var (
	// ErrSSORequired refuses non-SSO sign-in for non-owners when the workspace requires SSO.
	ErrSSORequired = domain.ErrSSORequired
	// ErrSettingsOnly refuses a person who cannot manage workspace settings.
	ErrSettingsOnly = domain.ErrSettingsOnly
	// ErrInvalidWorkspace refuses workspace settings that fail domain validation.
	ErrInvalidWorkspace = domain.ErrInvalidWorkspace
)

// WorkspaceStore reads and updates the single-row workspace settings.
type WorkspaceStore interface {
	MemberActor(ctx context.Context, id domain.UserID) (domain.RoleID, string, bool, []string, error)
	GetWorkspace(ctx context.Context, actor domain.UserID) (domain.Workspace, error)
	UpdateWorkspace(ctx context.Context, actor domain.UserID, allow string, next domain.WorkspaceUpdate, now time.Time) (domain.Workspace, error)
}

// Workspace reads and updates install-wide workspace settings.
type Workspace struct {
	Store WorkspaceStore
	Now   func() time.Time
}

func (w Workspace) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Get returns the current workspace settings for any active signed-in user.
func (w Workspace) Get(ctx context.Context, session Session) (domain.Workspace, error) {
	if _, err := w.actor(ctx, session.UserID); err != nil {
		return domain.Workspace{}, err
	}
	return w.Store.GetWorkspace(ctx, session.UserID)
}

// Update validates and saves workspace settings when the actor holds settings:manage.
func (w Workspace) Update(ctx context.Context, session Session, next domain.WorkspaceUpdate) (domain.Workspace, error) {
	actor, err := w.actor(ctx, session.UserID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if err := domain.MayManageWorkspace(actor); err != nil {
		return domain.Workspace{}, err
	}
	clean, err := domain.ParseWorkspaceUpdate(next)
	if err != nil {
		return domain.Workspace{}, err
	}
	names := make([]string, 0, len(actor.Permissions))
	for _, perm := range actor.Permissions {
		names = append(names, string(perm))
	}
	return w.Store.UpdateWorkspace(ctx, session.UserID, strings.Join(names, ","), clean, w.now())
}

func (w Workspace) actor(ctx context.Context, id domain.UserID) (domain.Subject, error) {
	roleID, system, joined, raw, err := w.Store.MemberActor(ctx, id)
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
