package identity

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/platform/mail"
)

// MemberService is the members use case. Commands use this name so they do not import the app package.
type MemberService = app.Members

// TaskGrant is one person or team grant on a task, with the subject's name.
// GrantWrite is one grant to store: a person or a team with a share level.
// Other contexts' share ports sit behind these.
type (
	TaskGrant  = app.TaskGrant
	GrantWrite = app.GrantWrite
)

// Members builds the members use case. The emails sealer stores addresses;
// without it invites and address lookups fail. Invite and recover links are
// emailed through sender, which may be nil; publicURL is the origin they open.
func Members(pool *pgxpool.Pool, emails EmailSealer, publicURL string, sender mail.Sender, log *slog.Logger) app.Members {
	return app.Members{Store: postgres.Passkeys{Pool: pool, Emails: emails}, Mail: sender, PublicURL: publicURL, Log: log}
}

// MountMembers registers the member routes.
func MountMembers(mux *http.ServeMux, members app.Members, sessions app.Service) {
	(httpapi.Members{Members: members, Sessions: sessions}).Mount(mux)
}
