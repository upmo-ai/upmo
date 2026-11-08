package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// shareJSON is the stored share of one task. team_id appears only when the
// visibility names a team.
type shareJSON struct {
	Visibility string       `json:"visibility"`
	TeamID     string       `json:"team_id,omitempty"`
	Members    []memberJSON `json:"members"`
}

// memberJSON is one member of a share: a person or a team with its role, and
// the name the server resolved.
type memberJSON struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
}

func shareBody(share app.Share) shareJSON {
	body := shareJSON{
		Visibility: share.Visibility,
		TeamID:     share.TeamID,
		Members:    make([]memberJSON, 0, len(share.Members)),
	}
	for _, member := range share.Members {
		body.Members = append(body.Members, memberJSON{
			SubjectType: member.SubjectType,
			SubjectID:   member.SubjectID,
			Name:        member.Name,
			Role:        member.Role,
		})
	}
	return body
}

func (h Handler) share(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Shares == nil || h.Grants == nil {
		writeError(w, http.StatusInternalServerError, "task")
		return
	}
	share, err := app.ReadShare(r.Context(), h.Shares, h.Grants, access, r.PathValue("id"))
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, shareBody(share))
}

func (h Handler) putShare(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.Shares == nil || h.Grants == nil {
		writeError(w, http.StatusInternalServerError, "task")
		return
	}
	var input struct {
		Visibility string `json:"visibility"`
		TeamID     string `json:"team_id"`
		Members    []struct {
			SubjectType string `json:"subject_type"`
			SubjectID   string `json:"subject_id"`
			Role        string `json:"role"`
		} `json:"members"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, "a task share needs a visibility")
		return
	}
	update := domain.ShareUpdate{Visibility: input.Visibility, TeamID: input.TeamID}
	for _, member := range input.Members {
		update.Grants = append(update.Grants, domain.ShareGrant{
			SubjectType: member.SubjectType,
			SubjectID:   member.SubjectID,
			Role:        member.Role,
		})
	}
	share, err := app.WriteShare(r.Context(), h.Shares, h.Grants, access, r.PathValue("id"), update)
	if refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, shareBody(share))
}
