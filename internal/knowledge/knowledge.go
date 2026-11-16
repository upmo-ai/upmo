// Package knowledge is the knowledge context. It embeds this context's SQL.
// A folder is the access scope. A source is the store of record.
package knowledge

import (
	"embed"
	"io/fs"

	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/knowledge/adapters/blob"
	"github.com/upmo-ai/upmo/internal/knowledge/adapters/convert"
	"github.com/upmo-ai/upmo/internal/knowledge/adapters/fetch"
	"github.com/upmo-ai/upmo/internal/knowledge/adapters/gateway"
	httpapi "github.com/upmo-ai/upmo/internal/knowledge/adapters/http"
	"github.com/upmo-ai/upmo/internal/knowledge/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
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

// Handler serves folders and sources to a signed-in person. Access is who
// asks, and Store keeps folders and sources in the knowledge schema.
type (
	Handler      = httpapi.Handler
	Access       = app.Access
	Store        = postgres.Store
	Embedder     = app.Embedder
	FolderLister = app.FolderLister
	Finder       = app.Finder
	Folder       = app.Folder
	Hit          = app.Hit
	Provenance   = app.Provenance
)

const (
	ProvenanceCurated = app.ProvenanceCurated
	ProvenanceWeb     = app.ProvenanceWeb
)

// Adapters for URL and file ingest: the body converter with its optional
// sidecar, the URL fetcher, the original upload volume and the gateway
// embed client.
type (
	Converter       = convert.Client
	Sidecar         = convert.Sidecar
	PassThroughText = app.PassThroughText
	Fetcher         = fetch.HTTPFetcher
	Volume          = blob.Volume
	EmbedClient     = gateway.Client
)

// NewStore returns the knowledge Store on pool. A nil enqueuer enqueues no jobs.
func NewStore(pool *pgxpool.Pool, enqueuer jobs.Enqueuer) Store {
	return postgres.Store{Pool: pool, Enqueuer: enqueuer}
}

// NewFetcher returns the URL fetcher for URL ingest.
func NewFetcher() Fetcher { return fetch.New() }

// OpenEmbedder returns a gateway embed client on embedRoute, signed with serviceKey.
func OpenEmbedder(baseURL, serviceKey, embedRoute string) (EmbedClient, error) {
	return gateway.Open(baseURL, serviceKey, embedRoute)
}

// ListFolders returns the folders access may see.
func ListFolders(ctx context.Context, book FolderLister, access Access) ([]Folder, error) {
	return app.ListFolders(ctx, book, access)
}

// Search returns the hits for query in one folder.
func Search(ctx context.Context, finder Finder, embedder Embedder, reranker app.Reranker, access Access, folderID, query string) ([]Hit, error) {
	return app.Search(ctx, finder, embedder, reranker, access, folderID, query)
}
