// Package httpapi serves knowledge folders, ingest and search for a signed-in person.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/knowledge/domain"
)

// Handler creates folders, ingests sources and searches through the existing store.
type Handler struct {
	Store   app.Store
	Pending app.PendingStore
	Folders app.FolderLister
	Sources app.SourceLister
	Finder  app.Finder
	Access  func(r *http.Request) (app.Access, bool)
	// Summariser and Embedder are optional. Nil still stores verbatim chunks.
	Summariser app.Summariser
	Embedder   app.Embedder
	Reranker   app.Reranker
	// Fetcher and Converter drive URL ingest. Nil refuses the URL route.
	Fetcher   app.URLFetcher
	Converter app.BodyConverter
	// AfterAccept runs URL processing. Nil starts a goroutine.
	AfterAccept func(func())
}

// Mount registers the knowledge routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/knowledge/folders", wrap(http.HandlerFunc(h.listFolders)))
	mux.Handle("POST /api/knowledge/folders", wrap(http.HandlerFunc(h.createFolder)))
	mux.Handle("GET /api/knowledge/folders/{id}/sources", wrap(http.HandlerFunc(h.listSources)))
	mux.Handle("GET /api/knowledge/folders/{id}/sources/{sourceID}", wrap(http.HandlerFunc(h.getSource)))
	mux.Handle("POST /api/knowledge/folders/{id}/sources", wrap(http.HandlerFunc(h.ingest)))
	mux.Handle("POST /api/knowledge/folders/{id}/sources/url", wrap(http.HandlerFunc(h.ingestURL)))
	mux.Handle("GET /api/knowledge/folders/{id}/search", wrap(http.HandlerFunc(h.search)))
}

func (h Handler) getSource(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	source, err := app.ViewSource(r.Context(), h.Sources, access, r.PathValue("id"), r.PathValue("sourceID"))
	if refuse(w, err) {
		return
	}
	body := map[string]any{
		"id":         source.ID,
		"title":      source.Title,
		"body":       source.Body,
		"kind":       source.Kind,
		"source_ref": source.SourceRef,
		"provenance": source.Provenance,
		"status":     source.Status,
		"revision":   source.Revision,
		"updated_at": source.UpdatedAt,
	}
	if source.Error != "" {
		body["error"] = source.Error
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) listSources(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	sources, err := app.ListSources(r.Context(), h.Sources, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	body := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		row := map[string]any{
			"id":         source.ID,
			"title":      source.Title,
			"kind":       source.Kind,
			"source_ref": source.SourceRef,
			"provenance": source.Provenance,
			"status":     source.Status,
			"revision":   source.Revision,
			"updated_at": source.UpdatedAt,
		}
		if source.Error != "" {
			row["error"] = source.Error
		}
		body = append(body, row)
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) listFolders(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	folders, err := app.ListFolders(r.Context(), h.Folders, access)
	if refuse(w, err) {
		return
	}
	body := make([]map[string]string, 0, len(folders))
	for _, folder := range folders {
		body = append(body, map[string]string{"id": folder.ID, "name": folder.Name})
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) createFolder(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		Name *string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Name == nil {
		writeError(w, http.StatusBadRequest, "a folder needs a name")
		return
	}
	id, err := app.CreateFolder(r.Context(), h.Store, access, *input.Name)
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h Handler) ingest(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		Title    *string `json:"title"`
		Body     *string `json:"body"`
		SourceID string  `json:"source_id"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Title == nil || input.Body == nil {
		writeError(w, http.StatusBadRequest, "a source needs a title and body")
		return
	}
	result, err := app.Ingest(r.Context(), h.Store, access, h.Summariser, h.Embedder, app.Draft{
		FolderID: r.PathValue("id"),
		SourceID: input.SourceID,
		Title:    *input.Title,
		Body:     *input.Body,
	})
	if refuse(w, err) {
		return
	}
	status := http.StatusCreated
	if result.Skipped {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"id":       result.ID,
		"revision": result.Revision,
		"chunks":   result.Chunks,
		"skipped":  result.Skipped,
	})
}

func (h Handler) ingestURL(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Pending == nil || h.Fetcher == nil || h.Converter == nil {
		writeError(w, http.StatusInternalServerError, "knowledge")
		return
	}
	var input struct {
		URL   *string `json:"url"`
		Title string  `json:"title"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.URL == nil {
		writeError(w, http.StatusBadRequest, "a source needs a url")
		return
	}
	folderID := r.PathValue("id")
	pending, err := app.IngestURL(r.Context(), h.Pending, access, app.URLDraft{
		FolderID: folderID,
		URL:      *input.URL,
		Title:    input.Title,
	})
	if refuse(w, err) {
		return
	}
	rawURL := strings.TrimSpace(*input.URL)
	title := input.Title
	sourceID := pending.ID
	run := h.AfterAccept
	if run == nil {
		run = func(fn func()) { go fn() }
	}
	run(func() {
		_ = app.ProcessURL(
			context.Background(),
			h.Pending,
			h.Store,
			access,
			h.Fetcher,
			h.Converter,
			h.Summariser,
			h.Embedder,
			folderID,
			sourceID,
			rawURL,
			title,
		)
	})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":     pending.ID,
		"status": pending.Status,
	})
}

func (h Handler) search(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	hits, err := app.Search(r.Context(), h.Finder, h.Embedder, h.Reranker, access, r.PathValue("id"), r.URL.Query().Get("q"))
	if refuse(w, err) {
		return
	}
	body := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		body = append(body, map[string]any{
			"source_id": hit.SourceID,
			"seq":       hit.Seq,
			"body":      hit.Body,
		})
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (app.Access, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return app.Access{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return app.Access{}, false
	}
	access, ok := h.Access(r)
	if !ok || access.UserID == "" || access.Allow == "" {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return app.Access{}, false
	}
	return access, true
}

func refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, app.ErrAccess):
		writeError(w, http.StatusForbidden, "unauthorised")
	case errors.Is(err, app.ErrGone):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, app.ErrFolder), errors.Is(err, app.ErrTitle), errors.Is(err, app.ErrName), errors.Is(err, app.ErrQuery), errors.Is(err, app.ErrURL), errors.Is(err, app.ErrURLBlocked), errors.Is(err, domain.ErrEmpty):
		writeError(w, http.StatusBadRequest, "the knowledge input was refused")
	default:
		writeError(w, http.StatusInternalServerError, "knowledge")
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, map[string]string{"error": text})
}
