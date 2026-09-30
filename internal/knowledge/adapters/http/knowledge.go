// Package httpapi serves knowledge folders, ingest and search for a signed-in person.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/knowledge/domain"
)

// Handler creates folders, ingests sources and searches through the existing store.
type Handler struct {
	Store  app.Store
	Finder app.Finder
	Access func(r *http.Request) (app.Access, bool)
	// Summariser and Embedder are optional. Nil still stores verbatim chunks.
	Summariser app.Summariser
	Embedder   app.Embedder
	Reranker   app.Reranker
}

// Mount registers the knowledge routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("POST /api/knowledge/folders", wrap(http.HandlerFunc(h.createFolder)))
	mux.Handle("POST /api/knowledge/folders/{id}/sources", wrap(http.HandlerFunc(h.ingest)))
	mux.Handle("GET /api/knowledge/folders/{id}/search", wrap(http.HandlerFunc(h.search)))
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
	case errors.Is(err, app.ErrFolder), errors.Is(err, app.ErrTitle), errors.Is(err, app.ErrName), errors.Is(err, app.ErrQuery), errors.Is(err, domain.ErrEmpty):
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
