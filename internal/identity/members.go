package identity

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/adapters/blob"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/platform/mail"
)

// MemberService is the members use case. Commands use this name so they do not import the app package.
type MemberService = app.Members

// AvatarVolume stores re-encoded user avatar PNG bytes on a confined volume.
type AvatarVolume = blob.Volume

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

// WithAvatars attaches an avatar blob volume rooted at dir to members and its store.
func WithAvatars(members app.Members, dir string) app.Members {
	if strings.TrimSpace(dir) == "" {
		return members
	}
	vol := blob.Volume{Root: dir}
	members.Blobs = vol
	if store, ok := members.Store.(postgres.Passkeys); ok {
		store.Blobs = vol
		members.Store = store
	}
	return members
}

// MountMembers registers the member routes.
func MountMembers(mux *http.ServeMux, members app.Members, sessions app.Service) {
	(httpapi.Members{Members: members, Sessions: sessions}).Mount(mux)
}

// MountAvatars registers the user avatar upload, removal, and read routes.
func MountAvatars(mux *http.ServeMux, members app.Members, sessions app.Service, pool *pgxpool.Pool) {
	(httpapi.Avatars{
		Members:  members,
		Sessions: sessions,
		Limit:    postgres.Passkeys{Pool: pool},
	}).Mount(mux)
}
