package domain

import "errors"

var (
	// ErrNotPermitted refuses a person who cannot manage members.
	ErrNotPermitted = errors.New("identity: only owners and admins manage members")
	// ErrOwnersOnly refuses a change to an owner by anyone except an owner.
	ErrOwnersOnly = errors.New("identity: only an owner can change the owner role")
	// ErrAlreadyMember refuses an invite of someone who is already enabled.
	ErrAlreadyMember = errors.New("identity: already a member")
	// ErrLastOwner refuses a change that would leave the install with no joined owner.
	ErrLastOwner = errors.New("identity: the install keeps at least one owner")
	// ErrSelf refuses a person acting on their own row.
	ErrSelf = errors.New("identity: you cannot do this to yourself")
	// ErrAlreadyJoined refuses a new invite link for someone who has joined.
	ErrAlreadyJoined = errors.New("identity: already joined")
	// ErrNotJoined refuses a reset link for someone who has not joined.
	ErrNotJoined = errors.New("identity: not joined yet")
	// ErrNoSuchMember is an unknown or disabled person.
	ErrNoSuchMember = errors.New("identity: no such member")
	// ErrMemberInput refuses an invite missing an email, a name or a role.
	ErrMemberInput = errors.New("identity: a person needs an email address, a name and a role")
)

// Member is an enabled person in the install.
type Member struct {
	ID        UserID
	Email     string
	Name      string
	RoleID    RoleID
	RoleName  string
	System    string
	Teams     []string
	Invited   bool
	AvatarURL *string
}

// MemberRole is a role an invite may name.
type MemberRole struct {
	ID          RoleID
	Name        string
	System      string
	Permissions []Permission
	Members     int
}

// MayManage reports whether actor may invite, re-link or remove a person
// whose system role key is targetSystem. An empty key is a custom role.
func MayManage(actor Subject, targetSystem string) error {
	if !has(known(actor.Permissions), PermMembersManage) {
		return ErrNotPermitted
	}
	if targetSystem == string(RoleOwner) && !has(known(actor.Permissions), PermOwnersManage) {
		return ErrOwnersOnly
	}
	return nil
}
