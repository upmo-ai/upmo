package audit

// AuthSignOut ends one signed-in session.
const AuthSignOut Action = "auth.sign_out"

// activityLogoff is the Authentication class activity of ending a session.
const activityLogoff = 1

func init() {
	register(AuthSignOut, "session", classAuthentication, activityLogoff)
}
