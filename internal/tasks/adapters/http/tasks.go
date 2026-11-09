// Package httpapi serves task transitions for a signed-in person.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/editorjwt"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Handler applies one task transition through the existing writer.
type Handler struct {
	Book app.Book
	List app.Lister
	View app.Reader
	// Approvals lists and decides the person's approvals. Nil answers an error
	// for the approval routes.
	Approvals app.ApprovalBook
	// Shares reads and stores one task's share: the task row under row
	// security and the visibility transaction. Nil answers an error for the
	// share routes.
	Shares app.ShareBook
	// Grants is the identity side of a share: a task's person and team grants
	// with names, replacing them, and the teams a person is on. Nil answers an
	// error for the share routes.
	Grants app.ShareGrants
	Access func(r *http.Request) (app.Access, bool)
	// Stream holds the open task event streams. Nil answers an error for the
	// stream route.
	Stream *app.StreamHub
	// Renew resolves the access of the person behind r again for an open
	// stream: the session looked up again, then the person's allow list and
	// shares. It reports false for a person who has lost access and an error
	// when that could not be found out. Nil keeps the access Access gave.
	Renew func(ctx context.Context, r *http.Request) (app.Access, bool, error)
	// Sandboxes starts a task's sandbox on Open and releases it when the
	// task ends. Open answers once the sandbox is created; provisioning runs
	// on after the response. Nil refuses any task that needs one.
	Sandboxes app.Sandboxes
	// Progress returns the in-process Preparing record. Nil answers not found
	// for GET /api/tasks/{id}/progress.
	Progress app.Progresses
	// PublicURL is the browser origin (UPMO_PUBLIC_URL) for editor origin checks.
	PublicURL string
	// Editor starts the code editor beside a held task sandbox. Nil refuses.
	Editor app.EditorSandboxes
	// EditorSigner mints the bound editor JWT. Nil refuses.
	EditorSigner app.EditorSigner
	// Now returns the current time. Nil uses time.Now().
	Now func() time.Time
}

// Mount registers the task routes. guard runs first.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/tasks", wrap(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/tasks/{id}/progress", wrap(http.HandlerFunc(h.progress)))
	mux.Handle("GET /api/tasks/{id}/stream", wrap(http.HandlerFunc(h.stream)))
	mux.Handle("GET /api/tasks/{id}/share", wrap(http.HandlerFunc(h.share)))
	mux.Handle("PUT /api/tasks/{id}/share", wrap(http.HandlerFunc(h.putShare)))
	mux.Handle("GET /api/tasks/{id}", wrap(http.HandlerFunc(h.get)))
	mux.Handle("POST /api/tasks", wrap(http.HandlerFunc(h.open)))
	mux.Handle("POST /api/tasks/{id}", wrap(http.HandlerFunc(h.step)))
	mux.Handle("POST /api/tasks/{id}/editor", wrap(http.HandlerFunc(h.editor)))
	mux.Handle("GET /api/approvals", wrap(http.HandlerFunc(h.approvals)))
	mux.Handle("POST /api/approvals/{id}/approve", wrap(http.HandlerFunc(h.approve)))
	mux.Handle("POST /api/approvals/{id}/deny", wrap(http.HandlerFunc(h.deny)))
}

func (h Handler) progress(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	rec, err := app.PreparingProgress(r.Context(), h.View, h.Progress, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"phase":  string(rec.Phase),
		"events": rec.Events,
	})
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.List == nil {
		writeError(w, http.StatusInternalServerError, "task")
		return
	}
	tasks, err := app.ListTasks(r.Context(), h.List, access)
	if refuse(w, err) {
		return
	}
	body := make([]summaryJSON, 0, len(tasks))
	for _, task := range tasks {
		body = append(body, summaryBody(task))
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) editor(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	out, err := app.OpenEditor(r.Context(), h.View, h.Editor, h.EditorSigner, h.PublicURL, access, r.PathValue("id"), now())
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"url":        out.URL,
		"token":      out.Token,
		"expires_at": out.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (h Handler) get(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	task, err := app.ViewTask(r.Context(), h.View, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, taskBody(task))
}

func (h Handler) open(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	// repo and tool_classes are optional. Either can make the task need a
	// sandbox.
	var input struct {
		Goal    *string  `json:"goal"`
		Repo    string   `json:"repo"`
		Classes []string `json:"tool_classes"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Goal == nil {
		writeError(w, http.StatusBadRequest, "a task needs a goal")
		return
	}
	task, err := app.OpenTask(r.Context(), h.Book, h.Sandboxes, access, app.Opening{
		Goal:    *input.Goal,
		Repo:    input.Repo,
		Classes: input.Classes,
	})
	if errors.Is(err, app.ErrSandbox) && task.ID != "" {
		// The task opened, then its sandbox failed and it was moved to Failed.
		body := taskJSON(task)
		body["error"] = "the task sandbox could not start"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, taskJSON(task))
}

func (h Handler) step(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var input struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
		Turn string `json:"turn"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, "a task needs a kind")
		return
	}
	kind := domain.Kind(strings.TrimSpace(input.Kind))
	switch kind {
	case domain.Advance, domain.Plan, domain.Fail, domain.Chat, domain.Pass:
	default:
		writeError(w, http.StatusBadRequest, "a task needs a kind")
		return
	}
	stepped, err := app.StepView(r.Context(), h.Book, h.View, h.Sandboxes, access, r.PathValue("id"), domain.Input{
		Kind: kind,
		Body: input.Body,
		Turn: input.Turn,
	})
	if refuse(w, err) {
		return
	}
	if stepped.Posted != nil {
		writeJSON(w, http.StatusCreated, eventBody(*stepped.Posted))
		return
	}
	writeJSON(w, http.StatusOK, taskBody(stepped.Task))
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

func taskJSON(task app.Task) map[string]string {
	return map[string]string{
		"id":     task.ID,
		"status": string(task.Status),
		"goal":   task.Goal,
	}
}

func refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var refusal domain.ShareRefusal
	if errors.As(err, &refusal) {
		writeError(w, http.StatusUnprocessableEntity, refusal.Text)
		return true
	}
	switch {
	case errors.Is(err, app.ErrSandboxUnset):
		writeError(w, http.StatusServiceUnavailable, "the task needs a sandbox and none is configured")
	case errors.Is(err, app.ErrNoTaskSandbox):
		writeError(w, http.StatusConflict, "the task holds no sandbox")
	case errors.Is(err, app.ErrSandbox):
		writeError(w, http.StatusServiceUnavailable, "the task sandbox could not start")
	case errors.Is(err, runtime.ErrUnsupported):
		writeError(w, http.StatusServiceUnavailable, "the code editor is not available on this install")
	case errors.Is(err, runtime.ErrEditor):
		writeError(w, http.StatusConflict, "the task cannot open a code editor in this status")
	case errors.Is(err, runtime.ErrRepo):
		writeError(w, http.StatusBadRequest, "the repository must be owner/name or an https URL")
	case errors.Is(err, app.ErrAccess):
		writeError(w, http.StatusForbidden, "unauthorised")
	case errors.Is(err, app.ErrGone):
		writeError(w, http.StatusNotFound, "no such task")
	case errors.Is(err, app.ErrPreparing):
		writeError(w, http.StatusNotFound, "no progress")
	case errors.Is(err, app.ErrNoApproval):
		writeError(w, http.StatusNotFound, "no such approval")
	case errors.Is(err, domain.ErrDecided):
		writeError(w, http.StatusConflict, "the approval was already decided")
	case errors.Is(err, domain.ErrClosed):
		writeError(w, http.StatusConflict, "the task is no longer waiting")
	case errors.Is(err, domain.ErrPending):
		writeError(w, http.StatusConflict, "the task already waits for an approval")
	case errors.Is(err, domain.ErrApproval):
		writeError(w, http.StatusBadRequest, "the approval input was refused")
	case errors.Is(err, app.ErrTask), errors.Is(err, domain.ErrInput), errors.Is(err, domain.ErrGoal), errors.Is(err, domain.ErrChat):
		writeError(w, http.StatusBadRequest, "the task input was refused")
	case errors.Is(err, domain.ErrTerminal), errors.Is(err, domain.ErrEvidence), errors.Is(err, domain.ErrTurn), errors.Is(err, domain.ErrExhausted), errors.Is(err, domain.ErrStatus):
		writeError(w, http.StatusConflict, "the task cannot take that step")
	case errors.Is(err, editorjwt.ErrNoKey):
		writeError(w, http.StatusInternalServerError, "task")
	default:
		writeError(w, http.StatusInternalServerError, "task")
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
