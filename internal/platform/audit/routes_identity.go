package audit

func identityRoutes() []entry {
	exempt := Route{Exempt: "starts a ceremony and changes nothing a person owns"}
	return []entry{
		{pattern: "DELETE /api/sessions", route: Route{Action: AuthSignOutAll}},
		{pattern: "DELETE /api/sessions/{id}", route: Route{Action: AuthSessionRevoke}},
		{pattern: "POST /api/members", route: Route{Action: MemberInvite}},
		{pattern: "POST /api/members/{id}/invite-link", route: Route{Action: MemberInviteLink}},
		{pattern: "POST /api/members/{id}/reset-link", route: Route{Action: MemberResetLink}},
		{pattern: "DELETE /api/members/{id}", route: Route{Action: MemberDisable}},
		{pattern: "PUT /api/members/{id}/role", route: Route{Action: MemberRoleChange}},
		{pattern: "POST /api/roles", route: Route{Action: RoleCreate}},
		{pattern: "PUT /api/roles/{id}", route: Route{Action: RoleUpdate}},
		{pattern: "POST /api/roles/{id}/duplicate", route: Route{Action: RoleCreate}},
		{pattern: "DELETE /api/roles/{id}", route: Route{Action: RoleDelete}},
		{pattern: "POST /api/teams", route: Route{Action: TeamCreate}},
		{pattern: "POST /api/teams/{id}/members", route: Route{Action: TeamMemberAdd}},
		{pattern: "DELETE /api/teams/{id}/members/{user}", route: Route{Action: TeamMemberRemove}},
		{pattern: "PUT /api/connector-grants/{connector}", route: Route{Action: ConnectorGrantSet}},
		{pattern: "DELETE /api/connector-grants/{connector}", route: Route{Action: ConnectorGrantRemove}},
		{pattern: "POST /api/tokens", route: Route{Action: TokenCreate}},
		{pattern: "DELETE /api/tokens/{id}", route: Route{Action: TokenRevoke}},
		{pattern: "POST /api/passkeys/sign-in/options", route: exempt},
		{pattern: "POST /api/passkeys/sign-in/finish", route: Route{Action: AuthSignIn}},
		{pattern: "POST /api/passkeys/link/options", route: exempt},
		{pattern: "POST /api/passkeys/link/finish", route: Route{Action: AuthSignIn}},
		{pattern: "POST /api/passkeys/step-up/options", route: exempt},
		{pattern: "POST /api/passkeys/step-up/finish", route: Route{Action: AuthStepUp}},
		{pattern: "POST /api/passkeys/options", route: exempt},
		{pattern: "POST /api/passkeys/finish", route: Route{Action: PasskeyAdd}},
		{pattern: "PATCH /api/passkeys/{id}", route: Route{Action: PasskeyRename}},
		{pattern: "DELETE /api/passkeys/{id}", route: Route{Action: PasskeyRemove}},
		{pattern: "POST /api/sign-in/password", route: Route{Action: AuthSignIn}},
		{pattern: "PUT /api/password", route: Route{Action: PasswordSet}},
		{pattern: "DELETE /api/password", route: Route{Action: PasswordRemove}},
		{pattern: "POST /api/session/verify/password", route: Route{Action: AuthStepUp}},
	}
}
