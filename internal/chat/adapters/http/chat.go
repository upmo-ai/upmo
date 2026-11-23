// Package httpapi serves chat open, list, say and read for a signed-in person.
package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
)

// Handler opens, lists, appends and reads chats through the existing book.
// Stream, when set, serves the chat stream; Renew then looks a person's access
// up again for the streams that stay open.
type Handler struct {
	Book      app.Book
	Converter app.BodyConverter
	Stream    *app.StreamHub
	Metrics   *metrics.Registry
	Access    func(r *http.Request) (app.Access, bool)
	Renew     func(ctx context.Context, r *http.Request) (app.Access, bool, error)
}

// Mount registers the chat routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/chats", wrap(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/chats", wrap(http.HandlerFunc(h.open)))
	mux.Handle("POST /api/chats/{id}", wrap(http.HandlerFunc(h.say)))
	mux.Handle("POST /api/chats/{id}/stop", wrap(http.HandlerFunc(h.stop)))
	mux.Handle("POST /api/chats/{id}/holds/{hold}/approve", wrap(http.HandlerFunc(h.approveHold)))
	mux.Handle("POST /api/chats/{id}/holds/{hold}/deny", wrap(http.HandlerFunc(h.denyHold)))
	mux.Handle("DELETE /api/chats/{id}", wrap(http.HandlerFunc(h.delete)))
	mux.Handle("GET /api/chats/{id}", wrap(http.HandlerFunc(h.read)))
	mux.Handle("POST /api/chats/{id}/lines/{line}/retry", wrap(http.HandlerFunc(h.retry)))
	mux.Handle("GET /api/chats/{id}/stream", wrap(http.HandlerFunc(h.stream)))
}

func (h Handler) approveHold(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		AllowForChat bool `json:"allow_for_chat"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "the chat input was refused")
		return
	}
	if refuse(w, app.ApproveHold(r.Context(), h.Book, access, r.PathValue("id"), r.PathValue("hold"), input.AllowForChat)) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h Handler) denyHold(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if refuse(w, app.DenyHold(r.Context(), h.Book, access, r.PathValue("id"), r.PathValue("hold"))) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h Handler) stop(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if refuse(w, app.Stop(r.Context(), h.Book, access, r.PathValue("id"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) delete(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if refuse(w, app.Delete(r.Context(), h.Book, access, r.PathValue("id"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) open(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	summary, err := app.Open(r.Context(), h.Book, access)
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, summaryBody(summary))
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	summaries, err := app.List(r.Context(), h.Book, access)
	if refuse(w, err) {
		return
	}
	body := make([]summaryJSON, 0, len(summaries))
	for _, summary := range summaries {
		body = append(body, summaryBody(summary))
	}
	writeJSON(w, http.StatusOK, body)
}

type attachmentInput struct {
	Name          string `json:"name"`
	Mime          string `json:"mime"`
	ContentBase64 string `json:"content_base64"`
}

func baseMediaType(raw string) string {
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(strings.ToLower(raw))
}

func (h Handler) say(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 30<<20)
	var (
		body           string
		picks          app.Picks
		rawAttachments []domain.RawAttachment
	)
	switch baseMediaType(r.Header.Get("Content-Type")) {
	case "multipart/form-data":
		if err := r.ParseMultipartForm(domain.MaxAttachmentBytes + 1<<20); err != nil { //nolint:gosec // body is capped by MaxBytesReader above
			writeError(w, http.StatusBadRequest, "the chat input was refused")
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		body = r.FormValue("body")
		picks = app.Picks{
			Route:      r.FormValue("route"),
			AgentID:    r.FormValue("agent_id"),
			PinAccount: r.FormValue("pin_account_id"),
			PinModel:   r.FormValue("pin_model"),
		}
		var headers []*multipart.FileHeader
		if r.MultipartForm != nil {
			headers = append(headers, r.MultipartForm.File["files"]...)
			headers = append(headers, r.MultipartForm.File["attachments"]...)
		}
		if len(headers) > domain.MaxAttachments {
			writeError(w, http.StatusBadRequest, "the chat input was refused")
			return
		}
		for _, hdr := range headers {
			raw, err := readMultipartAttachment(hdr)
			if err != nil {
				writeError(w, http.StatusBadRequest, "the chat input was refused")
				return
			}
			rawAttachments = append(rawAttachments, domain.RawAttachment{
				Name:         hdr.Filename,
				DeclaredMime: hdr.Header.Get("Content-Type"),
				Bytes:        raw,
			})
		}
	case "", "application/json":
		var input struct {
			pickInput
			Body        *string           `json:"body"`
			Attachments []attachmentInput `json:"attachments"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Body == nil {
			writeError(w, http.StatusBadRequest, "a chat needs a line")
			return
		}
		if len(input.Attachments) > domain.MaxAttachments {
			writeError(w, http.StatusBadRequest, "the chat input was refused")
			return
		}
		body = *input.Body
		picks = input.picks()
		for _, att := range input.Attachments {
			raw, err := base64.StdEncoding.DecodeString(att.ContentBase64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "the chat input was refused")
				return
			}
			rawAttachments = append(rawAttachments, domain.RawAttachment{
				Name:         att.Name,
				DeclaredMime: att.Mime,
				Bytes:        raw,
			})
		}
	default:
		writeError(w, http.StatusBadRequest, "the chat input was refused")
		return
	}
	if len(rawAttachments) > 0 {
		converted, err := app.ConvertAttachments(r.Context(), h.Converter, rawAttachments)
		if refuse(w, err) {
			return
		}
		picks.Attachments = converted
	}
	if err := app.Say(r.Context(), h.Book, access, r.PathValue("id"), body, picks); refuse(w, err) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func readMultipartAttachment(hdr *multipart.FileHeader) ([]byte, error) {
	part, err := hdr.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = part.Close() }()
	return io.ReadAll(io.LimitReader(part, domain.MaxAttachmentBytes+1))
}

// pickInput is what the composer sends with a line or a retry: the route, the
// model pinned as a provider account and a model id, and the agent. Each is
// optional.
type pickInput struct {
	Route   string    `json:"route"`
	Pin     *pinInput `json:"pin"`
	AgentID string    `json:"agent_id"`
}

type pinInput struct {
	AccountID string `json:"account_id"`
	Model     string `json:"model"`
}

func (p pickInput) picks() app.Picks {
	picks := app.Picks{Route: p.Route, AgentID: p.AgentID}
	if p.Pin != nil {
		picks.PinAccount, picks.PinModel = p.Pin.AccountID, p.Pin.Model
	}
	return picks
}

// retryPicks is the picks of a retry body, nil when it had none.
func retryPicks(input *pickInput) *app.Picks {
	if input == nil {
		return nil
	}
	picks := input.picks()
	return &picks
}

func (h Handler) retry(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	// No body reuses the picks stored on the line; any JSON object, even an
	// empty one, is the picks made now.
	var input *pickInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "the retry needs picks or nothing")
		return
	}
	if err := app.Retry(r.Context(), h.Book, access, r.PathValue("id"), r.PathValue("line"), retryPicks(input)); refuse(w, err) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h Handler) read(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	got, err := app.Read(r.Context(), h.Book, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, chatBody(got))
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (app.Access, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return app.Access{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return app.Access{}, false
	}
	access, ok := h.Access(r)
	if !ok || access.UserID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return app.Access{}, false
	}
	if access.Allow == "" {
		writeError(w, http.StatusForbidden, "unauthorized")
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
		writeError(w, http.StatusForbidden, "unauthorized")
	case errors.Is(err, app.ErrGone):
		writeError(w, http.StatusNotFound, "no such chat")
	case errors.Is(err, app.ErrSay):
		writeError(w, http.StatusForbidden, "needs a chat you can edit")
	case errors.Is(err, app.ErrNoLine):
		writeError(w, http.StatusNotFound, "no such line")
	case errors.Is(err, app.ErrNoHold):
		writeError(w, http.StatusNotFound, "no such hold")
	case errors.Is(err, app.ErrHoldDecided):
		writeError(w, http.StatusConflict, "that hold was already decided")
	case errors.Is(err, app.ErrRetry):
		writeError(w, http.StatusConflict, "that line cannot be retried")
	case errors.Is(err, app.ErrBusy):
		writeError(w, http.StatusConflict, "a reply is already on its way")
	case errors.Is(err, app.ErrReplies):
		writeError(w, http.StatusServiceUnavailable, "replies are off")
	case errors.Is(err, app.ErrChat), errors.Is(err, domain.ErrLine), errors.Is(err, domain.ErrRoute),
		errors.Is(err, domain.ErrPin), errors.Is(err, domain.ErrAgent),
		errors.Is(err, domain.ErrAttachment), errors.Is(err, domain.ErrAttachmentSize):
		writeError(w, http.StatusBadRequest, "the chat input was refused")
	case errors.Is(err, domain.ErrWriter):
		writeError(w, http.StatusConflict, "the chat cannot take that change")
	default:
		writeError(w, http.StatusInternalServerError, "chat")
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
