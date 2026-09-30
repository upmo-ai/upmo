package app

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/mail"
)

// MemberStore is the members tables.
type MemberStore interface {
	ListRoles(ctx context.Context) ([]domain.MemberRole, error)
	ListMembers(ctx context.Context) ([]domain.Member, error)
	MemberActor(ctx context.Context, id domain.UserID) (domain.RoleID, string, bool, []string, error)
	MemberTeams(ctx context.Context, id domain.UserID) ([]string, error)
	SharedObjects(ctx context.Context, objectType domain.ObjectType, user domain.UserID, teams []string, role domain.RoleID) ([]ObjectShare, error)
	RoleSystem(ctx context.Context, id domain.RoleID) (string, error)
	UserByEmail(ctx context.Context, email string) (domain.UserID, bool, error)
	InviteMember(ctx context.Context, email, name string, role domain.RoleID, handle, hash []byte, now, expires time.Time) (domain.Member, error)
	RemoveMember(ctx context.Context, id domain.UserID, now time.Time, rule func(system string) error) error
	ReplaceMemberLink(ctx context.Context, id domain.UserID, purpose string, hash []byte, expires time.Time, rule func(system string, invited bool) error) error
	Profile(ctx context.Context, user domain.UserID) (email, name string, err error)
}

// Members is who may manage the people in an install.
type Members struct {
	Store MemberStore
	Now   func() time.Time
	// Mail emails each invite and recover link. Nil sends nothing; the
	// token text is returned either way so the link can still be copied.
	Mail mail.Sender
	// PublicURL is the browser origin the emailed link opens.
	PublicURL string
	Log       *slog.Logger
}

func (m Members) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Roles lists the roles an invite may name.
func (m Members) Roles(ctx context.Context, session Session) ([]domain.MemberRole, error) {
	if _, err := m.actor(ctx, session.UserID); err != nil {
		return nil, err
	}
	return m.Store.ListRoles(ctx)
}

// List returns the enabled people.
func (m Members) List(ctx context.Context, session Session) ([]domain.Member, error) {
	if _, err := m.actor(ctx, session.UserID); err != nil {
		return nil, err
	}
	return m.Store.ListMembers(ctx)
}

// Invite creates or restores a person and returns the invite token text.
func (m Members) Invite(ctx context.Context, session Session, email, name string, role domain.RoleID) (domain.Member, string, error) {
	if err := m.fresh(session); err != nil {
		return domain.Member{}, "", err
	}
	email = strings.TrimSpace(email)
	name = strings.TrimSpace(name)
	if email == "" || !strings.Contains(email, "@") || name == "" || role == (domain.RoleID{}) {
		return domain.Member{}, "", domain.ErrMemberInput
	}
	actor, err := m.actor(ctx, session.UserID)
	if err != nil {
		return domain.Member{}, "", err
	}
	system, err := m.Store.RoleSystem(ctx, role)
	if err != nil {
		return domain.Member{}, "", err
	}
	if err := domain.MayManage(actor, system); err != nil {
		return domain.Member{}, "", err
	}
	text, hash, handle, err := newLink()
	if err != nil {
		return domain.Member{}, "", err
	}
	now := m.now()
	member, err := m.Store.InviteMember(ctx, email, name, role, handle, hash, now, now.Add(domain.InviteLifetime))
	if err != nil {
		return domain.Member{}, "", err
	}
	m.send(ctx, "invite", member.ID, member.Email, text)
	return member, text, nil
}

// InviteLink replaces the invite link for someone who has not joined.
func (m Members) InviteLink(ctx context.Context, session Session, target domain.UserID) (string, error) {
	return m.replace(ctx, session, target, "invite")
}

// ResetLink replaces the recover link for someone who has joined.
func (m Members) ResetLink(ctx context.Context, session Session, target domain.UserID) (string, error) {
	return m.replace(ctx, session, target, "recover")
}

// Remove disables a person other than the actor.
func (m Members) Remove(ctx context.Context, session Session, target domain.UserID) error {
	if err := m.fresh(session); err != nil {
		return err
	}
	if session.UserID == target {
		return domain.ErrSelf
	}
	actor, err := m.actor(ctx, session.UserID)
	if err != nil {
		return err
	}
	return m.Store.RemoveMember(ctx, target, m.now(), func(system string) error {
		return domain.MayManage(actor, system)
	})
}

// Recover issues a recover link for an enabled person. An unknown email and a
// disabled person are the same answer.
func (m Members) Recover(ctx context.Context, email string) (string, error) {
	id, ok, err := m.Store.UserByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", domain.ErrNoSuchMember
	}
	text, hash, _, err := newLink()
	if err != nil {
		return "", err
	}
	now := m.now()
	err = m.Store.ReplaceMemberLink(ctx, id, "recover", hash, now.Add(domain.RecoverLifetime), nil)
	if err != nil {
		return "", err
	}
	m.sendTo(ctx, "recover", id, text)
	return text, nil
}

func (m Members) replace(ctx context.Context, session Session, target domain.UserID, purpose string) (string, error) {
	if err := m.fresh(session); err != nil {
		return "", err
	}
	if session.UserID == target {
		return "", domain.ErrSelf
	}
	actor, err := m.actor(ctx, session.UserID)
	if err != nil {
		return "", err
	}
	text, hash, _, err := newLink()
	if err != nil {
		return "", err
	}
	life, ok := domain.LinkLifetime(purpose)
	if !ok {
		return "", domain.ErrMemberInput
	}
	err = m.Store.ReplaceMemberLink(ctx, target, purpose, hash, m.now().Add(life), func(system string, invited bool) error {
		if err := domain.MayManage(actor, system); err != nil {
			return err
		}
		if purpose == "invite" && !invited {
			return domain.ErrAlreadyJoined
		}
		if purpose == "recover" && invited {
			return domain.ErrNotJoined
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	m.sendTo(ctx, purpose, target, text)
	return text, nil
}

// sendTo emails the link to the person's stored address.
func (m Members) sendTo(ctx context.Context, purpose string, id domain.UserID, token string) {
	if m.Mail == nil {
		return
	}
	email, _, err := m.Store.Profile(ctx, id)
	if err != nil {
		m.warn(purpose, id, err)
		return
	}
	m.send(ctx, purpose, id, email, token)
}

// send emails the link. A failed send is logged and never fails the request:
// the caller still returns the token text for copying. The token is never
// logged.
func (m Members) send(ctx context.Context, purpose string, id domain.UserID, email, token string) {
	if m.Mail == nil {
		return
	}
	link := linkURL(m.PublicURL, token)
	var err error
	if purpose == "invite" {
		err = m.Mail.SendInvite(ctx, email, link)
	} else {
		err = m.Mail.SendRecover(ctx, email, link)
	}
	if err != nil {
		m.warn(purpose, id, errors.New(strings.ReplaceAll(err.Error(), token, "[token]")))
	}
}

func (m Members) warn(purpose string, id domain.UserID, err error) {
	if m.Log != nil {
		m.Log.Warn("link email not sent", "purpose", purpose, "member", id.String(), "err", err.Error())
	}
}

// TaskAllow is the task permissions a joined person holds, comma-separated.
// The task writer stores that list on the row and checks it on every call.
func (m Members) TaskAllow(ctx context.Context, id domain.UserID) (string, error) {
	subject, err := m.actor(ctx, id)
	if err != nil {
		return "", err
	}
	var names []string
	for _, permission := range subject.Permissions {
		name := string(permission)
		if strings.HasPrefix(name, "task:") {
			names = append(names, name)
		}
	}
	return strings.Join(names, ","), nil
}

// ArtifactAllow is the artifact permissions a joined person holds, comma-separated.
func (m Members) ArtifactAllow(ctx context.Context, id domain.UserID) (string, error) {
	subject, err := m.actor(ctx, id)
	if err != nil {
		return "", err
	}
	var names []string
	for _, permission := range subject.Permissions {
		name := string(permission)
		if strings.HasPrefix(name, "artifact:") {
			names = append(names, name)
		}
	}
	return strings.Join(names, ","), nil
}

// ChatAllow is the chat permissions a joined person holds, comma-separated.
func (m Members) ChatAllow(ctx context.Context, id domain.UserID) (string, error) {
	subject, err := m.actor(ctx, id)
	if err != nil {
		return "", err
	}
	var names []string
	for _, permission := range subject.Permissions {
		name := string(permission)
		if strings.HasPrefix(name, "chat:") {
			names = append(names, name)
		}
	}
	return strings.Join(names, ","), nil
}

// ObjectShare is one row from SharedObjectsSQL.
type ObjectShare struct {
	ObjectID string
	Level    domain.Level
}

// Shares is a joined person's teams and the objects of one type shared with
// them, as Postgres uuid array literals for upmo.teams, upmo.shared_view and
// upmo.shared_edit. Any level can view; only editor can edit.
type Shares struct {
	Teams      string
	SharedView string
	SharedEdit string
}

// ShareAccess loads the person's teams and shares for one object type. The
// object stores write the result into each transaction.
func (m Members) ShareAccess(ctx context.Context, id domain.UserID, objectType domain.ObjectType) (Shares, error) {
	subject, err := m.actor(ctx, id)
	if err != nil {
		return Shares{}, err
	}
	teams, err := m.Store.MemberTeams(ctx, id)
	if err != nil {
		return Shares{}, err
	}
	rows, err := m.Store.SharedObjects(ctx, objectType, id, teams, subject.RoleID)
	if err != nil {
		return Shares{}, err
	}
	var view, edit []string
	for _, row := range rows {
		view = append(view, row.ObjectID)
		if row.Level == domain.LevelEditor {
			edit = append(edit, row.ObjectID)
		}
	}
	return Shares{
		Teams:      domain.UUIDArray(teams),
		SharedView: domain.UUIDArray(view),
		SharedEdit: domain.UUIDArray(edit),
	}, nil
}

func (m Members) actor(ctx context.Context, id domain.UserID) (domain.Subject, error) {
	roleID, system, joined, raw, err := m.Store.MemberActor(ctx, id)
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
		Kind:        domain.KindUser,
		UserID:      id,
		RoleID:      roleID,
		Joined:      true,
		Permissions: permissions,
	}, nil
}

func (m Members) fresh(session Session) error {
	if !domain.Fresh(session.SignedIn, m.now()) {
		return ErrStale
	}
	return nil
}

func permissionsFor(system string, raw []string) ([]domain.Permission, error) {
	if system != "" {
		return domain.SystemPermissions(domain.Role(system))
	}
	var out []domain.Permission
	for _, name := range raw {
		permission, err := domain.ParsePermission(name)
		if err != nil {
			continue
		}
		out = append(out, permission)
	}
	return out, nil
}

func newLink() (string, []byte, []byte, error) {
	token, text, err := domain.NewToken()
	if err != nil {
		return "", nil, nil, err
	}
	handle := make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		return "", nil, nil, err
	}
	return text, domain.Hash(token), handle, nil
}
