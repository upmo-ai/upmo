// Package tasks is the tasks context. It embeds this context's SQL.
// A task moves through one pure step, and one writer appends the event.
// A signed-in person opens and moves a task over HTTP.
package tasks

import (
	"embed"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/platform/jobs"
	httpapi "github.com/upmo-ai/upmo/internal/tasks/adapters/http"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
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

// Handler opens and moves tasks for a signed-in person. Access is who asks,
// and Store keeps tasks in the tasks schema.
type (
	Handler      = httpapi.Handler
	Access       = app.Access
	Store        = postgres.Store
	EditorSigner = app.EditorSigner
	EditorTokens = app.EditorTokens
)

// The proposal scout and what it reads.
type (
	ScoutDeps    = app.ScoutDeps
	Proposer     = app.Proposer
	MemoryFact   = app.MemoryFact
	KnowledgeHit = app.KnowledgeHit
)

// ErrRetry asks River to run a scout job again.
var ErrRetry = app.ErrRetry

// NewStore returns the tasks Store on pool. A nil enqueuer enqueues no jobs.
func NewStore(pool *pgxpool.Pool, enqueuer jobs.Enqueuer) Store {
	return postgres.Store{Pool: pool, Enqueuer: enqueuer}
}

// NewEditorTokens derives the editor token signer from the install service keys.
func NewEditorTokens(current, previous string) (EditorTokens, error) {
	return app.NewEditorTokens(current, previous)
}

// AddScoutWorker registers the proposal scout worker on workers.
func AddScoutWorker(workers *river.Workers, deps ScoutDeps) { app.AddScoutWorker(workers, deps) }
