package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Share is the stored share of one task: its visibility, the team a team
// visibility names, and the members with the names the server resolved.
type Share struct {
	Visibility string
	TeamID     string
	Members    []ShareMember
}

// ShareMember is one member of a share. The owner entry is first.
type ShareMember struct {
	SubjectType string
	SubjectID   string
	Name        string
	Role        string
}

// TaskShare is the task row behind a share: the owner, the visibility with
// its team, and whether identity lets this person edit the task.
type TaskShare struct {
	OwnerID    string
	Visibility string
	TeamID     string
	Editable   bool
}

// ShareBook is the tasks side of a share. ShareRow answers under row
// security, ErrGone for a task this person cannot see. SaveShare replaces the
// grants and stores visibility in one transaction and seals task.share.
type ShareBook interface {
	ShareRow(ctx context.Context, access Access, taskID string) (TaskShare, error)
	SaveShare(ctx context.Context, access Access, taskID, visibility, teamID string, grants []domain.ShareGrant, grantedBy string) error
}

// ShareGrants is the identity side of a share. The context owns none of this
// data, so it reads and writes it through this port.
type ShareGrants interface {
	// TaskGrants returns the person and team grants of one task with the names
	// of their subjects. Role grants are not listed.
	TaskGrants(ctx context.Context, taskID string) ([]ShareMember, error)
	// ReplaceTaskGrants replaces the person and team grants of one task with
	// grants, recording grantedBy on every new one. Role grants are untouched.
	ReplaceTaskGrants(ctx context.Context, taskID string, grants []domain.ShareGrant, grantedBy string) error
	// Names returns the display names of the enabled people in persons and the
	// names of the teams in teams. An id that is no enabled member, or no
	// team, is missing from the answer.
	Names(ctx context.Context, persons, teams []string) (personNames, teamNames map[string]string, err error)
	// TeamsOf returns the ids of the teams the person is on.
	TeamsOf(ctx context.Context, person string) ([]string, error)
}

// ReadShare returns the stored share of one task: the owner entry first, then
// the person and team grants with names. The owner and the people who can
// edit the task may read it; a viewer is refused.
func ReadShare(ctx context.Context, book ShareBook, grants ShareGrants, access Access, taskID string) (Share, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Share{}, ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return Share{}, ErrTask
	}
	row, err := book.ShareRow(ctx, access, taskID)
	if err != nil {
		return Share{}, err
	}
	if !row.Editable && row.OwnerID != access.UserID {
		return Share{}, ErrAccess
	}
	return storedShare(ctx, grants, row, taskID)
}

// WriteShare replaces the share of one task. Only the owner may write: the
// update is validated, then the store replaces grants, stores visibility, and
// seals the audit event in one transaction, so a retry with the same body
// converges.
func WriteShare(ctx context.Context, book ShareBook, grants ShareGrants, access Access, taskID string, update domain.ShareUpdate) (Share, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Share{}, ErrAccess
	}
	id, err := uuid.Parse(taskID)
	if err != nil {
		return Share{}, ErrTask
	}
	// One spelling of the id, so the per-task lock in the grants write is the
	// same lock whatever the letter case of the URL.
	taskID = id.String()
	row, err := book.ShareRow(ctx, access, taskID)
	if err != nil {
		return Share{}, err
	}
	// The visibility write needs edit too, so refuse before the grants change.
	if row.OwnerID != access.UserID || !row.Editable {
		return Share{}, ErrAccess
	}
	stored, err := domain.ValidateShare(update, row.OwnerID)
	if err != nil {
		return Share{}, err
	}
	teamID, err := shareTeam(ctx, grants, update, row.OwnerID, stored)
	if err != nil {
		return Share{}, err
	}
	if err := book.SaveShare(ctx, access, taskID, update.Visibility, teamID, stored, access.UserID); err != nil {
		return Share{}, err
	}
	return storedShare(ctx, grants, TaskShare{OwnerID: row.OwnerID, Visibility: update.Visibility, TeamID: teamID}, taskID)
}

// shareTeam validates the people and teams the grants name and resolves the
// visibility team: team visibility needs the named team when there is one,
// else the owner's only team; private and everyone store none.
func shareTeam(ctx context.Context, grants ShareGrants, update domain.ShareUpdate, ownerID string, stored []domain.ShareGrant) (string, error) {
	var persons, teams []string
	for _, grant := range stored {
		if grant.SubjectType == domain.SharePerson {
			persons = append(persons, grant.SubjectID)
		} else {
			teams = append(teams, grant.SubjectID)
		}
	}
	personNames, teamNames, err := grants.Names(ctx, persons, teams)
	if err != nil {
		return "", err
	}
	for _, id := range persons {
		if _, ok := personNames[id]; !ok {
			return "", domain.ShareRefusal{Text: "that person is not a member"}
		}
	}
	for _, id := range teams {
		if _, ok := teamNames[id]; !ok {
			return "", domain.ShareRefusal{Text: "that team does not exist"}
		}
	}
	if update.Visibility != domain.VisibilityTeam {
		return "", nil
	}
	owned, err := grants.TeamsOf(ctx, ownerID)
	if err != nil {
		return "", err
	}
	onTeam := func(id string) bool {
		for _, team := range owned {
			if team == id {
				return true
			}
		}
		return false
	}
	if update.TeamID != "" {
		// The canonical text, so the same team in capitals still matches.
		id, err := uuid.Parse(update.TeamID)
		if err != nil {
			return "", domain.ShareRefusal{Text: "that team does not exist"}
		}
		update.TeamID = id.String()
		if !onTeam(update.TeamID) {
			return "", domain.ShareRefusal{Text: "the owner does not belong to that team"}
		}
		return update.TeamID, nil
	}
	switch len(owned) {
	case 1:
		return owned[0], nil
	case 0:
		return "", domain.ShareRefusal{Text: "the owner belongs to no team"}
	default:
		return "", domain.ShareRefusal{Text: "the owner belongs to more than one team"}
	}
}

// storedShare assembles the share of one task: the owner entry first, with
// the name resolved, then the stored grants.
func storedShare(ctx context.Context, grants ShareGrants, row TaskShare, taskID string) (Share, error) {
	members, err := grants.TaskGrants(ctx, taskID)
	if err != nil {
		return Share{}, err
	}
	personNames, _, err := grants.Names(ctx, []string{row.OwnerID}, nil)
	if err != nil {
		return Share{}, err
	}
	name := personNames[row.OwnerID]
	if name == "" {
		name = row.OwnerID
	}
	share := Share{
		Visibility: row.Visibility,
		TeamID:     row.TeamID,
		Members: []ShareMember{{
			SubjectType: domain.SharePerson,
			SubjectID:   row.OwnerID,
			Name:        name,
			Role:        domain.ShareOwner,
		}},
	}
	share.Members = append(share.Members, members...)
	return share, nil
}
