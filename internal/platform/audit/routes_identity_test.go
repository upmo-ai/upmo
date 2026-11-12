package audit

import "testing"

// TestIdentityRoutesAreInstrumented keeps the identity area strict: every
// route carries an action, or the exemption a ceremony start needs.
func TestIdentityRoutesAreInstrumented(t *testing.T) {
	for _, e := range identityRoutes() {
		if e.route.Action == "" && e.route.Exempt == "" {
			t.Errorf("%s has neither an action nor an exemption", e.pattern)
		}
		if e.route.Action != "" {
			if _, ok := catalog[e.route.Action]; !ok {
				t.Errorf("%s covers %s, which is not in the catalog", e.pattern, e.route.Action)
			}
		}
	}
}

// TestCeremonyStartsAreTheOnlyExemptions guards the exemption: a route may opt
// out only when it starts a WebAuthn ceremony and changes nothing a person owns.
func TestCeremonyStartsAreTheOnlyExemptions(t *testing.T) {
	want := map[string]bool{
		"POST /api/passkeys/sign-in/options": true,
		"POST /api/passkeys/link/options":    true,
		"POST /api/passkeys/step-up/options": true,
		"POST /api/passkeys/options":         true,
	}
	for _, e := range identityRoutes() {
		if e.route.Exempt == "" {
			continue
		}
		if !want[e.pattern] {
			t.Errorf("%s is exempt but starts no ceremony", e.pattern)
		}
	}
	for pattern := range want {
		if !hasRoute(pattern) {
			t.Errorf("%s is no longer registered", pattern)
		}
	}
}

func hasRoute(pattern string) bool {
	for _, e := range identityRoutes() {
		if e.pattern == pattern {
			return true
		}
	}
	return false
}
