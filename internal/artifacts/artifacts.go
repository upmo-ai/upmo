// Package artifacts is the artifacts context. It embeds this context's SQL.
// An artifact outlives the sandbox that produced it, on a Compose volume or in
// object storage, and erasure removes it.
// A served artifact always sends nosniff, and HTML is never the type.
package artifacts

import (
	"embed"
	"io/fs"
)

// Schema is the Postgres schema this context owns.
const Schema = "artifacts"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("artifacts: embed migrations: " + err.Error())
	}
	return files
}
