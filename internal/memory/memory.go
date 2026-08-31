// Package memory is the memory context. It embeds this context's SQL.
// Facts stay personal: row security is the signed-in person, not identity.protect.
package memory

import (
	"embed"
	"io/fs"
)

// Schema is the Postgres schema this context owns.
const Schema = "memory"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("memory: embed migrations: " + err.Error())
	}
	return files
}
