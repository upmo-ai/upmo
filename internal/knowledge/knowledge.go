// Package knowledge is the knowledge context. It embeds this context's SQL.
// A folder is the access scope. A source is the store of record.
package knowledge

import (
	"embed"
	"io/fs"
)

// Schema is the Postgres schema this context owns.
const Schema = "knowledge"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("knowledge: embed migrations: " + err.Error())
	}
	return files
}
