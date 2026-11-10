package artifacts

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Handler lists and serves artifacts through the existing store and volume.
type Handler struct {
	Catalog Catalog
	Blobs   Blobs
	Access  func(r *http.Request) (Access, bool)
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
	items, err := ListForTask(r.Context(), h.Catalog, access, r.PathValue("id"))
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
	_, headers, body, err := OpenForDownload(r.Context(), h.Catalog, h.Blobs, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set(HeaderType, headers.Type)
	w.Header().Set(HeaderDisposition, headers.Disposition)
	w.Header().Set(HeaderSniff, headers.Sniff)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (Access, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return Access{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return Access{}, false
	}
	access, ok := h.Access(r)
	if !ok || access.UserID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return Access{}, false
	}
	if access.Allow == "" {
		writeError(w, http.StatusForbidden, "unauthorised")
		return Access{}, false
	}
	return access, true
}

func refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrArtifact):
		writeError(w, http.StatusForbidden, "unauthorised")
	case errors.Is(err, ErrGone), errors.Is(err, ErrNoBlob), errors.Is(err, ErrBlob):
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
