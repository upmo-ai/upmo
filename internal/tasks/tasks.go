// Package tasks is the tasks context. It embeds this context's SQL.
// A task moves through one pure step, and one writer appends the event.
// A read-only scout proposes at most five tasks a day, and only as notifications.
package tasks

import (
	"embed"
	"io/fs"
)

// Schema is the Postgres schema this context owns.
const Schema = "tasks"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("tasks: embed migrations: " + err.Error())
	}
	return files
}
