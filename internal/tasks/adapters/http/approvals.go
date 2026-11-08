package httpapi

import (
	"net/http"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// approvalJSON is an approval as the person who decides it sees it.
type approvalJSON struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	Action string `json:"action"`
	State  string `json:"state"`
}

func approvalBody(approval app.Approval) approvalJSON {
	return approvalJSON{
		ID:     approval.ID,
		TaskID: approval.TaskID,
		Title:  approval.Title,
		Action: approval.Action,
		State:  string(approval.State),
	}
}

func (h Handler) approvals(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Approvals == nil {
		writeError(w, http.StatusInternalServerError, "task")
		return
	}
	listed, err := app.ListApprovals(r.Context(), h.Approvals, access)
	if refuse(w, err) {
		return
	}
	body := make([]approvalJSON, 0, len(listed))
	for _, approval := range listed {
		body = append(body, approvalBody(approval))
	}
	writeJSON(w, http.StatusOK, body)
}

func (h Handler) approve(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, domain.Approve)
}

func (h Handler) deny(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, domain.Deny)
}

func (h Handler) decide(w http.ResponseWriter, r *http.Request, kind domain.Kind) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Approvals == nil {
		writeError(w, http.StatusInternalServerError, "task")
		return
	}
	approval, err := app.DecideApproval(r.Context(), h.Approvals, access, r.PathValue("id"), kind)
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, approvalBody(approval))
}
