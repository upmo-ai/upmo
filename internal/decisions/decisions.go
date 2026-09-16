// Package decisions is the decisions context. It embeds this context's SQL.
// A decide route changes model only when the labelled gate passes.
// Agent dispatch and route intent are choice routes scored on labelled accuracy.
package decisions

import (
	"embed"
	"io/fs"
)

// Schema is the Postgres schema this context owns.
const Schema = "decisions"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("decisions: embed migrations: " + err.Error())
	}
	return files
}
