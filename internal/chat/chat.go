// Package chat is the chat context. It embeds this context's SQL.
// A chat is an owned object, and the profile block rides with it.
package chat

import (
	"embed"
	"io/fs"
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
