// Package identity is the identity context. It embeds this context's SQL.
// Session rules live in the domain; the table and the cookie live in the adapters.
// MemberService.ShareAccess loads a person's teams and shares for one object
// type, and the object stores write them into each transaction.
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
