package audit

import "context"

// Credential types of the system actor's background writes.
const (
	// CredentialRun is one queued job's writes: an automation fire, a task run.
	CredentialRun = "run"
	// CredentialService is a periodic service's writes: the delta sweep.
	CredentialService = "service"
)

// WithRunner seeds the scope of a background write: the system actor with the
// given run or service credential. A context that already carries a request
// scope keeps it, so a writer shared by a route and a job records the person
// when a person drove the request, and the system when the job did.
func WithRunner(ctx context.Context, credential string) context.Context {
	if _, ok := ctx.Value(scopeKey{}).(*scope); ok {
		return ctx
	}
	return context.WithValue(ctx, scopeKey{}, &scope{
		actor: Actor{Type: "system", CredentialType: credential},
	})
}
