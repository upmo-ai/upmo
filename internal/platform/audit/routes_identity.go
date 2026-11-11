package audit

func identityRoutes() []entry {
	todo := Route{Todo: "UPM-270"}
	return []entry{
		{pattern: "DELETE /api/sessions", route: todo},
		{pattern: "DELETE /api/sessions/{id}", route: todo},
		{pattern: "POST /api/members", route: todo},
		{pattern: "POST /api/members/{id}/invite-link", route: todo},
		{pattern: "POST /api/members/{id}/reset-link", route: todo},
		{pattern: "DELETE /api/members/{id}", route: todo},
		{pattern: "PUT /api/members/{id}/role", route: todo},
		{pattern: "POST /api/roles", route: todo},
		{pattern: "PUT /api/roles/{id}", route: todo},
		{pattern: "POST /api/roles/{id}/duplicate", route: todo},
		{pattern: "DELETE /api/roles/{id}", route: todo},
		{pattern: "POST /api/teams", route: todo},
		{pattern: "POST /api/teams/{id}/members", route: todo},
		{pattern: "DELETE /api/teams/{id}/members/{user}", route: todo},
		{pattern: "PUT /api/connector-grants/{connector}", route: todo},
		{pattern: "DELETE /api/connector-grants/{connector}", route: todo},
		{pattern: "POST /api/tokens", route: todo},
		{pattern: "DELETE /api/tokens/{id}", route: todo},
		{pattern: "POST /api/passkeys/sign-in/options", route: todo},
		{pattern: "POST /api/passkeys/sign-in/finish", route: todo},
		{pattern: "POST /api/passkeys/link/options", route: todo},
		{pattern: "POST /api/passkeys/link/finish", route: todo},
		{pattern: "POST /api/passkeys/step-up/options", route: todo},
		{pattern: "POST /api/passkeys/step-up/finish", route: todo},
		{pattern: "POST /api/passkeys/options", route: todo},
		{pattern: "POST /api/passkeys/finish", route: todo},
		{pattern: "PATCH /api/passkeys/{id}", route: todo},
		{pattern: "DELETE /api/passkeys/{id}", route: todo},
		{pattern: "POST /api/sign-in/password", route: todo},
		{pattern: "PUT /api/password", route: todo},
		{pattern: "DELETE /api/password", route: todo},
		{pattern: "POST /api/session/verify/password", route: todo},
	}
}
