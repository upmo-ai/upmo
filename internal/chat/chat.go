// Package chat is the chat context. It embeds this context's SQL.
// A chat is an owned object. The profile stays in memory and is read when a turn is built.
package chat

import (
	"embed"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/chat/adapters/http"
	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
)

// Schema is the Postgres schema this context owns.
const Schema = "chat"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("chat: embed migrations: " + err.Error())
	}
	return files
}

// Handler serves chats to a signed-in person. Access is who asks, and Store
// keeps chats in the chat schema.
type (
	Handler = httpapi.Handler
	Access  = app.Access
	Store   = postgres.Store
)

// NewStore returns the chat Store on pool. A nil enqueuer enqueues no jobs.
func NewStore(pool *pgxpool.Pool, enqueuer jobs.Enqueuer) Store {
	return postgres.Store{Pool: pool, Enqueuer: enqueuer}
}
