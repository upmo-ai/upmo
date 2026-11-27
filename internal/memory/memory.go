// Package memory is the memory context. It embeds this context's SQL.
// Facts stay personal: row security is the signed-in person, not identity.protect.
package memory

import (
	"embed"
	"encoding/json"
	"io/fs"
	"time"

	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/memory/adapters/gateway"
	httpapi "github.com/upmo-ai/upmo/internal/memory/adapters/http"
	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
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

// Handler serves the personalisation page. Facts keeps facts in the memory
// schema, and GatewayClient proposes and embeds through the gateway.
type (
	Handler        = httpapi.Handler
	Facts          = postgres.Facts
	GatewayClient  = gateway.Client
	Editor         = app.Editor
	Proposer       = app.Proposer
	Embedder       = app.Embedder
	Page           = app.Page
	Recall         = app.Recall
	RetrievalStore = app.RetrievalStore
	Reranker       = app.Reranker
	ProfileReader  = app.ProfileReader
)

const SearchMemoryName = app.SearchMemoryName

func SearchMemoryTool() (string, json.RawMessage) { return app.SearchMemoryTool() }
func SearchMemory(ctx context.Context, store RetrievalStore, embedder Embedder, reranker Reranker, userID, query string, asOf time.Time) (Recall, error) {
	return app.SearchMemory(ctx, store, embedder, reranker, userID, query, asOf)
}

// ErrRetry reports a gateway failure worth retrying.
var (
	ErrRetry     = app.ErrRetry
	ErrClaim     = app.ErrClaim
	ErrDuplicate = app.ErrDuplicate
)

// AddFact stores a user-confirmed personal fact (trust = 'user').
func AddFact(ctx context.Context, editor Editor, userID, claim string, at time.Time) (string, error) {
	return app.AddFact(ctx, editor, userID, claim, at)
}

// NewFacts returns the memory Facts on pool.
func NewFacts(pool *pgxpool.Pool) Facts { return postgres.Facts{Pool: pool} }

// OpenGateway returns a gateway client that proposes on proposeRoute and
// embeds on embedRoute, signed with serviceKey.
func OpenGateway(baseURL, serviceKey, proposeRoute, embedRoute string) (GatewayClient, error) {
	return gateway.Open(baseURL, serviceKey, proposeRoute, embedRoute)
}

// AddExtractWorker registers the memory extract worker on workers.
func AddExtractWorker(workers *river.Workers, store app.Store, propose func(userID string) Proposer, embed func(userID string) Embedder) {
	app.AddExtractWorker(workers, store, propose, embed)
}

// LoadPage returns the profile and facts a person sees.
func LoadPage(ctx context.Context, editor Editor, userID string) (Page, error) {
	return app.LoadPage(ctx, editor, userID)
}

// Profile returns one person's profile body under row security.
func Profile(ctx context.Context, reader ProfileReader, userID string) (string, error) {
	return app.Profile(ctx, reader, userID)
}

// SystemPrefix places the profile after the standing instructions, as the
// cacheable chat system text.
func SystemPrefix(instructions, profile string) string {
	return app.SystemPrefix(instructions, profile)
}
