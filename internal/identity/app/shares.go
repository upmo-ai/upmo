package app

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// TaskGrant is one person or team grant on a task, with the name of its
// subject resolved.
type TaskGrant struct {
	SubjectType string // person or team
	SubjectID   string
	Name        string
	Level       string // editor or viewer
}

// GrantWrite is one grant to store on a task: a person or a team with a share
// level, editor or viewer.
type GrantWrite struct {
	SubjectType string // person or team
	SubjectID   string
	Level       string // editor or viewer
}

// TaskGrants returns the person and team grants of one task with the names of
// their subjects. Role grants are not listed. The tasks context reads this
// through its own share port; identity owns the rows.
func (m Members) TaskGrants(ctx context.Context, taskID string) ([]TaskGrant, error) {
	return m.Store.TaskGrants(ctx, taskID)
}

// ReplaceTaskGrants replaces the person and team grants of one task: grants
// not listed are deleted, new ones are inserted with grantedBy, and the level
// changes where it differs. Role grants are untouched.
func (m Members) ReplaceTaskGrants(ctx context.Context, taskID string, grants []GrantWrite, grantedBy domain.UserID) error {
	return m.Store.ReplaceTaskGrants(ctx, taskID, grants, grantedBy)
}

// ReplaceTaskGrantsTx is ReplaceTaskGrants inside a transaction the caller owns.
func (m Members) ReplaceTaskGrantsTx(ctx context.Context, tx pgx.Tx, taskID string, grants []GrantWrite, grantedBy domain.UserID) error {
	return m.Store.ReplaceTaskGrantsTx(ctx, tx, taskID, grants, grantedBy)
}

// NamesOf returns the display names of the enabled people in persons and the
// names of the teams in teams. An id that is no enabled member, or no team,
// is missing from the answer: that is how a caller refuses an unknown one.
func (m Members) NamesOf(ctx context.Context, persons, teams []string) (map[string]string, map[string]string, error) {
	return m.Store.NamesOf(ctx, persons, teams)
}

// TeamsOf returns the ids of the teams the person is on.
func (m Members) TeamsOf(ctx context.Context, person domain.UserID) ([]string, error) {
	return m.Store.MemberTeams(ctx, person)
}
