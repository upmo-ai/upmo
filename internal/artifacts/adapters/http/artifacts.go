// Package httpapi serves artifact list and download for a signed-in person.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/artifacts"
)

// Handler lists and serves artifacts through the existing store and volume.
type Handler struct {
	Catalog artifacts.Catalog
	Blobs   artifacts.Blobs
	Access  func(r *http.Request) (artifacts.Access, bool)
}

// Mount registers the artifact routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/tasks/{id}/artifacts", wrap(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/artifacts/{id}", wrap(http.HandlerFunc(h.download)))
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	items, err := artifacts.ListForTask(r.Context(), h.Catalog, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	body := make([]map[string]string, 0, len(items))
	for _, item := range items {
		body = append(body, map[string]string{
			"id":    item.ID,
			"name":  item.Name,
			"store": item.Store,
		})
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) download(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	_, headers, body, err := artifacts.OpenForDownload(r.Context(), h.Catalog, h.Blobs, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	defer body.Close()
	w.Header().Set(artifacts.HeaderType, headers.Type)
	w.Header().Set(artifacts.HeaderDisposition, headers.Disposition)
	w.Header().Set(artifacts.HeaderSniff, headers.Sniff)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (artifacts.Access, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return artifacts.Access{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return artifacts.Access{}, false
	}
	access, ok := h.Access(r)
	if !ok || access.UserID == "" || access.Allow == "" {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return artifacts.Access{}, false
	}
	return access, true
}

func refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, artifacts.ErrArtifact):
		writeError(w, http.StatusForbidden, "unauthorised")
	case errors.Is(err, artifacts.ErrGone), errors.Is(err, artifacts.ErrNoBlob), errors.Is(err, artifacts.ErrBlob):
		writeError(w, http.StatusNotFound, "no such artifact")
	default:
		writeError(w, http.StatusInternalServerError, "artifact")
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
