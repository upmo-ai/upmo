// Package identity is the identity context. It embeds this context's SQL.
// The domain package does no I/O; sessions, passkeys and tokens are later work.
package identity

import (
	"embed"
	"io/fs"
)

// Schema is the Postgres schema this context owns.
const Schema = "identity"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("identity: embed migrations: " + err.Error())
	}
	return files
}
