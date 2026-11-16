package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// RoleStore is the role rows and the member assignment.
type RoleStore interface {
	RoleByID(ctx context.Context, id domain.RoleID) (domain.MemberRole, error)
	CreateRole(ctx context.Context, name string, permissions []string) (domain.RoleID, error)
	ChangeRole(ctx context.Context, id domain.RoleID, name string, permissions []string) error
	DeleteRole(ctx context.Context, id domain.RoleID) error
	SetMemberRole(ctx context.Context, actorTokenHash []byte, user domain.UserID, role domain.RoleID, rule func(current, next string, permissions []domain.Permission) error) error
	MemberActor(ctx context.Context, id domain.UserID) (domain.RoleID, string, bool, []string, error)
}

// Roles creates, changes and assigns roles.
type Roles struct {
	Store RoleStore
	Now   func() time.Time
	Log   *slog.Logger
}

func (r Roles) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Create stores a custom role from permissions the actor holds.
func (r Roles) Create(ctx context.Context, session Session, name string, names []string) (domain.RoleID, error) {
	if err := r.fresh(session); err != nil {
		return domain.RoleID{}, r.refuse("role create refused", session, err)
	}
	actor, err := r.actor(ctx, session.UserID)
	if err != nil {
		return domain.RoleID{}, r.refuse("role create refused", session, err)
	}
	clean, permissions, err := domain.ParseRoleSet(name, names)
	if err != nil {
		return domain.RoleID{}, r.refuse("role create refused", session, err)
	}
	if err := domain.MayGrant(actor, permissions); err != nil {
		return domain.RoleID{}, r.refuse("role create refused", session, err)
	}
	id, err := r.Store.CreateRole(ctx, clean, permissionNames(permissions))
	if err != nil {
		return domain.RoleID{}, r.refuse("role create refused", session, err)
	}
	return id, nil
}

// Change updates a custom role. Added permissions must be ones the actor holds.
func (r Roles) Change(ctx context.Context, session Session, id domain.RoleID, name string, names []string) error {
	if err := r.fresh(session); err != nil {
		return r.refuse("role change refused", session, err)
	}
	actor, err := r.actor(ctx, session.UserID)
	if err != nil {
		return r.refuse("role change refused", session, err)
	}
	role, err := r.Store.RoleByID(ctx, id)
	if err != nil {
		return r.refuse("role change refused", session, err)
	}
	if role.System != "" {
		return r.refuse("role change refused", session, domain.ErrSystemRole)
	}
	if actor.RoleID == id {
		return r.refuse("role change refused", session, domain.ErrSelf)
	}
	clean, permissions, err := domain.ParseRoleSet(name, names)
	if err != nil {
		return r.refuse("role change refused", session, err)
	}
	if err := domain.MayGrant(actor, domain.Added(role.Permissions, permissions)); err != nil {
		return r.refuse("role change refused", session, err)
	}
	if err := r.Store.ChangeRole(ctx, id, clean, permissionNames(permissions)); err != nil {
		return r.refuse("role change refused", session, err)
	}
	return nil
}

// Duplicate copies a role into a new custom role, without owner-only permissions.
func (r Roles) Duplicate(ctx context.Context, session Session, id domain.RoleID, name string) (domain.RoleID, error) {
	if err := r.fresh(session); err != nil {
		return domain.RoleID{}, r.refuse("role duplicate refused", session, err)
	}
	actor, err := r.actor(ctx, session.UserID)
	if err != nil {
		return domain.RoleID{}, r.refuse("role duplicate refused", session, err)
	}
	source, err := r.Store.RoleByID(ctx, id)
	if err != nil {
		return domain.RoleID{}, r.refuse("role duplicate refused", session, err)
	}
	clean, permissions, err := domain.ParseRoleSet(name, permissionNames(domain.WithoutOwnerOnly(source.Permissions)))
	if err != nil {
		return domain.RoleID{}, r.refuse("role duplicate refused", session, err)
	}
	if err := domain.MayGrant(actor, permissions); err != nil {
		return domain.RoleID{}, r.refuse("role duplicate refused", session, err)
	}
	created, err := r.Store.CreateRole(ctx, clean, permissionNames(permissions))
	if err != nil {
		return domain.RoleID{}, r.refuse("role duplicate refused", session, err)
	}
	return created, nil
}

// Delete removes a custom role that nobody holds.
func (r Roles) Delete(ctx context.Context, session Session, id domain.RoleID) error {
	if err := r.fresh(session); err != nil {
		return r.refuse("role delete refused", session, err)
	}
	actor, err := r.actor(ctx, session.UserID)
	if err != nil {
		return r.refuse("role delete refused", session, err)
	}
	role, err := r.Store.RoleByID(ctx, id)
	if err != nil {
		return r.refuse("role delete refused", session, err)
	}
	if role.System != "" {
		return r.refuse("role delete refused", session, domain.ErrSystemRole)
	}
	if actor.RoleID == id {
		return r.refuse("role delete refused", session, domain.ErrSelf)
	}
	if err := domain.MayGrant(actor, nil); err != nil {
		return r.refuse("role delete refused", session, err)
	}
	if err := r.Store.DeleteRole(ctx, id); err != nil {
		return r.refuse("role delete refused", session, err)
	}
	return nil
}

// Set gives one member a role.
func (r Roles) Set(ctx context.Context, session Session, member domain.UserID, role domain.RoleID) error {
	if err := r.fresh(session); err != nil {
		return r.refuse("member role refused", session, err)
	}
	if session.UserID == member {
		return r.refuse("member role refused", session, domain.ErrSelf)
	}
	actor, err := r.actor(ctx, session.UserID)
	if err != nil {
		return r.refuse("member role refused", session, err)
	}
	err = r.Store.SetMemberRole(ctx, session.TokenHash, member, role, func(current, next string, permissions []domain.Permission) error {
		return domain.MayAssign(actor, current, next, permissions)
	})
	if err != nil {
		return r.refuse("member role refused", session, err)
	}
	return nil
}

func (r Roles) actor(ctx context.Context, id domain.UserID) (domain.Subject, error) {
	roleID, system, joined, raw, err := r.Store.MemberActor(ctx, id)
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

func (r Roles) fresh(session Session) error {
	if !domain.Fresh(session.SignedIn, r.now()) {
		return ErrStale
	}
	return nil
}

// refuse keeps the call sites uniform. The audit middleware seals a refused
// mutation as a denied event, so nothing is logged here.
func (r Roles) refuse(_ string, _ Session, err error) error {
	return err
}

func permissionNames(permissions []domain.Permission) []string {
	out := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		out = append(out, string(permission))
	}
	return out
}
