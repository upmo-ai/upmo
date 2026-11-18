package agentloop

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Visibility mirrors the Postgres visibility enum on tasks and chats.
type Visibility string

const (
	VisibilityPrivate  Visibility = "private"
	VisibilityTeam     Visibility = "team"
	VisibilityEveryone Visibility = "everyone"
)

// AccessScope describes the acting person and the object they are running on.
type AccessScope struct {
	UserID           string
	Allow            string
	UserTeams        []string
	ObjectID         string
	ObjectVisibility Visibility
	VisibilityTeamID string
	SharedView       string
	SharedEdit       string
}

// NarrowedRLS holds the transaction-local Postgres RLS settings and the
// narrowed Gateway TeamID derived from AccessScope.
type NarrowedRLS struct {
	UserID        string
	Allow         string
	Teams         []string
	TeamsPGArray  string
	SharedView    string
	SharedEdit    string
	GatewayTeamID string
}

// TxExecer executes SQL statements inside an open Postgres transaction.
type TxExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// NarrowRLS computes RLS narrowing for the active object:
//   - upmo.shared_view / upmo.shared_edit keep only the active object for
//     every visibility; shares of other objects are cleared ("").
//   - When ObjectVisibility is VisibilityTeam, upmo.teams is set to
//     actual_user_teams ∩ {visibility_team_id}, never granting team
//     membership if a shared editor is not in visibility_team_id.
//   - GatewayTeamID is set to VisibilityTeamID (team objects only) so
//     gateway filters identity.teams_of(user_id) to TeamID.
func NarrowRLS(scope AccessScope) NarrowedRLS {
	if scope.ObjectVisibility != VisibilityTeam {
		teams := cleanUUIDList(scope.UserTeams)
		return NarrowedRLS{
			UserID:       scope.UserID,
			Allow:        scope.Allow,
			Teams:        teams,
			TeamsPGArray: pgUUIDArray(teams),
			SharedView:   keepOnlyObject(scope.SharedView, scope.ObjectID),
			SharedEdit:   keepOnlyObject(scope.SharedEdit, scope.ObjectID),
		}
	}

	var narrowedTeams []string
	if scope.VisibilityTeamID != "" && slices.Contains(scope.UserTeams, scope.VisibilityTeamID) {
		narrowedTeams = []string{scope.VisibilityTeamID}
	}

	return NarrowedRLS{
		UserID:        scope.UserID,
		Allow:         scope.Allow,
		Teams:         narrowedTeams,
		TeamsPGArray:  pgUUIDArray(narrowedTeams),
		SharedView:    keepOnlyObject(scope.SharedView, scope.ObjectID),
		SharedEdit:    keepOnlyObject(scope.SharedEdit, scope.ObjectID),
		GatewayTeamID: scope.VisibilityTeamID,
	}
}

// ApplyRLSTx sets transaction-local upmo.* settings on tx using set_config(..., true).
func ApplyRLSTx(ctx context.Context, tx TxExecer, rls NarrowedRLS) error {
	teams := rls.TeamsPGArray
	if teams == "" {
		teams = "{}"
	}
	sharedView := arrayOrEmpty(rls.SharedView)
	sharedEdit := arrayOrEmpty(rls.SharedEdit)
	_, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', $3, true),
		       set_config('upmo.shared_view', $4, true),
		       set_config('upmo.shared_edit', $5, true)`,
		rls.UserID, rls.Allow, teams, sharedView, sharedEdit)
	if err != nil {
		return fmt.Errorf("agentloop: apply rls: %w", err)
	}
	return nil
}

func keepOnlyObject(pgArray, objectID string) string {
	objectID = strings.TrimSpace(objectID)
	if objectID == "" {
		return ""
	}
	trimmed := strings.TrimSpace(pgArray)
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	if trimmed == "" {
		return ""
	}
	for _, part := range strings.Split(trimmed, ",") {
		if strings.TrimSpace(part) == objectID {
			return "{" + objectID + "}"
		}
	}
	return ""
}

func cleanUUIDList(in []string) []string {
	var out []string
	seen := make(map[string]bool, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func pgUUIDArray(ids []string) string {
	if len(ids) == 0 {
		return "{}"
	}
	return "{" + strings.Join(ids, ",") + "}"
}

func arrayOrEmpty(literal string) string {
	if strings.TrimSpace(literal) == "" {
		return "{}"
	}
	return literal
}
