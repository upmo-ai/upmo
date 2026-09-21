// Package decisions is the decisions context. It embeds this context's SQL.
// A decide route changes model only when the labelled gate passes.
// Agent dispatch and route intent are choice routes scored on labelled accuracy.
// A memory check is a yes-or-no route scored on precision.
// A fact worth keeping is a yes-or-no route scored on recall.
// A tool surface is a filter route that must keep every tool the turn calls.
// A permission second opinion is a choice route that may escalate and must never relax.
// A model tier route is a choice route scored on canary success parity.
// Folder filing is a choice route scored on labelled accuracy.
// Channel triage is a choice route scored on labelled accuracy.
// A rerank route is scored on NDCG.
// A result confidence route is a yes-or-no route scored on precision.
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
