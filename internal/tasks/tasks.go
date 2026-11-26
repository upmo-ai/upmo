// Package tasks is the tasks context. It embeds this context's SQL.
// A task moves through one pure step, and one writer appends the event.
// A signed-in person opens and moves a task over HTTP.
package tasks

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/platform/jobs"
	httpapi "github.com/upmo-ai/upmo/internal/tasks/adapters/http"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
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
	Book         = app.Book
	Lister       = app.Lister
	TemplateBook = app.TemplateBook
	Automations  = app.Automations
	Sandboxes    = app.Sandboxes
	Opening      = app.Opening
	Task         = app.Task
	TaskSummary  = app.TaskSummary
	Template     = domain.Template
	Input        = domain.Input
	EditorSigner = app.EditorSigner
	EditorTokens = app.EditorTokens
)

// Open is the task transition input kind that opens a new task.
const Open = domain.Open

// ErrAccess is a missing person or an empty allow list.
var ErrAccess = app.ErrAccess

// OpenTask opens a task in Preparing (delegating to app.OpenTask -> app.ApplyTransition).
// Passing boxes == nil opens a task that does not provision a sandbox at open time.
func OpenTask(ctx context.Context, book Book, boxes Sandboxes, access Access, open Opening) (Task, error) {
	return app.OpenTask(ctx, book, boxes, access, open)
}

// ApplyTransition validates the caller and applies one task transition.
func ApplyTransition(ctx context.Context, book Book, access Access, taskID string, input Input) (Task, error) {
	return app.ApplyTransition(ctx, book, access, taskID, input)
}

// ListTasks validates the caller and returns visible tasks newest update first.
func ListTasks(ctx context.Context, book Lister, access Access) ([]TaskSummary, error) {
	return app.ListTasks(ctx, book, access)
}

// SaveTemplate validates the caller and template and stores it under row security.
func SaveTemplate(ctx context.Context, book TemplateBook, access Access, id string, in Template) (string, error) {
	return app.SaveTemplate(ctx, book, access, id, in)
}

// SaveAutomation validates the caller and trigger and records an active automation for templateID.
func SaveAutomation(ctx context.Context, book Automations, access Access, templateID, kind, spec string) (string, error) {
	return app.SaveAutomation(ctx, book, access, templateID, kind, spec)
}

// The task share port and what it carries. Another context's data sits behind
// ShareGrants, so cmd wires an adapter to it.
type (
	ShareGrants = app.ShareGrants
	ShareMember = app.ShareMember
	ShareGrant  = domain.ShareGrant
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

// The task event streams of one process.
type (
	StreamHub    = app.StreamHub
	StreamConfig = app.StreamConfig
	Stream       = app.Stream
	StreamSpec   = app.StreamSpec
)

// NewStreamHub returns the stream hub of this process. It reads through pool
// and listens for new events on one connection of its own made from conn,
// which is the same database the pool uses. A zero cfg takes the defaults.
// The caller starts it with Start and stops it with Close.
func NewStreamHub(pool *pgxpool.Pool, conn *pgx.ConnConfig, log *slog.Logger, cfg StreamConfig) *StreamHub {
	return app.NewStreamHub(postgres.Store{Pool: pool}, postgres.Listener{Config: conn, Log: log}, log, cfg)
}

// NewStore returns the tasks Store on pool. runner logs in as upmo_runner and
// writes the runner columns upmo_app may not. A nil enqueuer enqueues no jobs.
func NewStore(pool, runner *pgxpool.Pool, enqueuer jobs.Enqueuer) Store {
	return postgres.Store{Pool: pool, Runner: runner, Enqueuer: enqueuer}
}

// NewEditorTokens builds the editor token signer from the editor keys.
func NewEditorTokens(current, previous string) (EditorTokens, error) {
	return app.NewEditorTokens(current, previous)
}

// AddScoutWorker registers the proposal scout worker on workers.
func AddScoutWorker(workers *river.Workers, deps ScoutDeps) { app.AddScoutWorker(workers, deps) }

// AutomationExists reports whether id names an existing automation.
func AutomationExists(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	return postgres.AutomationExists(ctx, pool, id)
}
