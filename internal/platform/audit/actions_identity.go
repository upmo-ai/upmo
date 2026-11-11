package audit

// The identity catalog speaks the OCSF Identity and Access Management classes:
// Authentication is already pinned in actions.go, Account Change covers the
// person's own rows, and User Access Management covers team membership and
// connector grants.
const (
	classAccountChange        = 3001
	classUserAccessManagement = 3005
)

// OCSF activity ids of the Authentication class (3002).
const (
	activityLogon  = 1
	activityLogoff = 2
)

// OCSF activity ids of the Account Change class (3001).
const (
	activityCreate         = 1
	activityPasswordChange = 3
	activityPasswordReset  = 4
	activityDisable        = 5
	activityDelete         = 6
)

// AuthSignOut ends one signed-in session.
const AuthSignOut Action = "auth.sign_out"

// Sign-in and session actions are Authentication events.
const (
	// AuthSignIn opens a session, by passkey, link or password.
	AuthSignIn Action = "auth.sign_in"
	// AuthStepUp re-proves a live session before a sensitive change.
	AuthStepUp Action = "auth.step_up"
	// AuthSignOutAll ends every session of the acting person.
	AuthSignOutAll Action = "auth.sign_out_all"
	// AuthSessionRevoke ends one session the person chose.
	AuthSessionRevoke Action = "auth.session_revoke"
)

// Member, role, token, passkey and password actions are Account Change events.
const (
	MemberInvite     Action = "member.invite"
	MemberInviteLink Action = "member.invite_link"
	MemberResetLink  Action = "member.reset_link"
	MemberDisable    Action = "member.disable"
	MemberRoleChange Action = "member.role_change"
	RoleCreate       Action = "role.create"
	RoleUpdate       Action = "role.update"
	RoleDelete       Action = "role.delete"
	TokenCreate      Action = "token.create"
	TokenRevoke      Action = "token.revoke"
	PasskeyAdd       Action = "passkey.add"
	PasskeyRename    Action = "passkey.rename"
	PasskeyRemove    Action = "passkey.remove"
	PasswordSet      Action = "password.set"
	PasswordRemove   Action = "password.remove"
)

// Team membership and connector grant actions are User Access Management events.
const (
	TeamCreate           Action = "team.create"
	TeamMemberAdd        Action = "team.member_add"
	TeamMemberRemove     Action = "team.member_remove"
	ConnectorGrantSet    Action = "connector_grant.set"
	ConnectorGrantRemove Action = "connector_grant.remove"
)

func init() {
	register(AuthSignIn, "session", classAuthentication, activityLogon, "method")
	register(AuthStepUp, "session", classAuthentication, activityUnknown, "method")
	register(AuthSignOut, "session", classAuthentication, activityLogoff)
	register(AuthSignOutAll, "user", classAuthentication, activityLogoff)
	register(AuthSessionRevoke, "session", classAuthentication, activityLogoff)

	register(MemberInvite, "member", classAccountChange, activityCreate)
	register(MemberInviteLink, "member", classAccountChange, activityUnknown)
	register(MemberResetLink, "member", classAccountChange, activityPasswordReset)
	register(MemberDisable, "member", classAccountChange, activityDisable, "sessions_ended")
	register(MemberRoleChange, "member", classAccountChange, activityUnknown, "role_id")
	register(RoleCreate, "role", classAccountChange, activityCreate)
	register(RoleUpdate, "role", classAccountChange, activityUnknown, "permissions")
	register(RoleDelete, "role", classAccountChange, activityDelete)
	register(TokenCreate, "token", classAccountChange, activityCreate, "scope", "expires_at")
	register(TokenRevoke, "token", classAccountChange, activityDelete)
	register(PasskeyAdd, "passkey", classAccountChange, activityCreate)
	register(PasskeyRename, "passkey", classAccountChange, activityUnknown)
	register(PasskeyRemove, "passkey", classAccountChange, activityDelete)
	register(PasswordSet, "user", classAccountChange, activityPasswordChange)
	register(PasswordRemove, "user", classAccountChange, activityUnknown)

	register(TeamCreate, "team", classUserAccessManagement, activityUnknown)
	register(TeamMemberAdd, "team", classUserAccessManagement, activityUnknown, "user_id")
	register(TeamMemberRemove, "team", classUserAccessManagement, activityUnknown, "user_id")
	register(ConnectorGrantSet, "connector_grant", classUserAccessManagement, activityUnknown, "resource_scope", "changes")
	register(ConnectorGrantRemove, "connector_grant", classUserAccessManagement, activityUnknown)
}
