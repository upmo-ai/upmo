// Package chat is the chat context. It embeds this context's SQL.
// A chat is an owned object. The profile stays in memory and is read when a turn is built.
package chat

import (
	"embed"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/chat/adapters/gateway"
	httpapi "github.com/upmo-ai/upmo/internal/chat/adapters/http"
	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
)

// Schema is the Postgres schema this context owns.
const Schema = "chat"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("chat: embed migrations: " + err.Error())
	}
	return files
}

// Handler serves chats to a signed-in person. Access is who asks, and Store
// keeps chats in the chat schema.
type (
	Handler = httpapi.Handler
	Access  = app.Access
	Store   = postgres.Store
)

// Agent is the profile a reply runs as.
type Agent = app.Agent

// ErrAgentNotFound is returned by AgentLookup when an agent is unavailable.
var ErrAgentNotFound = app.ErrAgentNotFound

// The reply to a chat line streams from a model while it is written.
type (
	ReplyJobArgs = app.ReplyJobArgs
	ReplyConfig  = app.ReplyConfig
	ReplyDeps    = app.ReplyDeps
	StreamHub    = app.StreamHub
	StreamConfig = app.StreamConfig
	Stream       = app.Stream
)

// ReplyQueue is the River queue of reply jobs; give it workers of its own.
const ReplyQueue = app.ReplyQueue

// NewStore returns the chat Store on pool. A nil enqueuer enqueues no jobs.
func NewStore(pool *pgxpool.Pool, enqueuer jobs.Enqueuer) Store {
	return postgres.Store{Pool: pool, Enqueuer: enqueuer}
}

// OpenModel returns the model a reply is streamed from, which is the gateway
// at baseURL, signed with serviceKey.
func OpenModel(baseURL, serviceKey string) (app.Model, error) {
	client, err := gateway.Open(baseURL, serviceKey)
	if err != nil {
		return nil, err
	}
	return gateway.Model{Client: client}, nil
}

// AddReplyWorker registers the worker that answers chat lines on workers.
func AddReplyWorker(workers *river.Workers, deps ReplyDeps) error {
	return app.AddReplyWorker(workers, deps)
}

// AddSweepWorker registers the worker that deletes the deltas of replies that
// never finished, and SweepPeriodic is the job that runs it.
func AddSweepWorker(workers *river.Workers, store Store) {
	app.AddSweepWorker(workers, store)
}

// SweepPeriodic runs the sweep every ten minutes.
func SweepPeriodic() *river.PeriodicJob { return app.SweepPeriodic() }

// NewStreamHub returns the chat stream hub of this process. It reads through
// pool and hears of new rows on the connection conn describes.
func NewStreamHub(pool *pgxpool.Pool, conn *pgx.ConnConfig, log *slog.Logger, cfg StreamConfig) *StreamHub {
	return app.NewStreamHub(postgres.Store{Pool: pool}, postgres.Listener{Config: conn, Log: log}, log, cfg)
}
