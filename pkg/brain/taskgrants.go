package brain

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/tasks"
)

// taskGrants is the identity side of a task share. The tasks context owns the
// port and identity owns the rows, so brain joins them.
type taskGrants struct {
	members identity.MemberService
}

var _ tasks.ShareGrants = taskGrants{}

func (g taskGrants) TaskGrants(ctx context.Context, taskID string) ([]tasks.ShareMember, error) {
	rows, err := g.members.TaskGrants(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]tasks.ShareMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, tasks.ShareMember{
			SubjectType: row.SubjectType,
			SubjectID:   row.SubjectID,
			Name:        row.Name,
			Role:        row.Level,
		})
	}
	return out, nil
}

func (g taskGrants) ReplaceTaskGrants(ctx context.Context, taskID string, grants []tasks.ShareGrant, grantedBy string) error {
	by, err := identity.ParseUserID(grantedBy)
	if err != nil {
		return err
	}
	writes := make([]identity.GrantWrite, 0, len(grants))
	for _, grant := range grants {
		writes = append(writes, identity.GrantWrite{
			SubjectType: grant.SubjectType,
			SubjectID:   grant.SubjectID,
			Level:       grant.Role,
		})
	}
	return g.members.ReplaceTaskGrants(ctx, taskID, writes, by)
}

func (g taskGrants) ReplaceTaskGrantsTx(ctx context.Context, tx pgx.Tx, taskID string, grants []tasks.ShareGrant, grantedBy string) error {
	by, err := identity.ParseUserID(grantedBy)
	if err != nil {
		return err
	}
	writes := make([]identity.GrantWrite, 0, len(grants))
	for _, grant := range grants {
		writes = append(writes, identity.GrantWrite{
			SubjectType: grant.SubjectType,
			SubjectID:   grant.SubjectID,
			Level:       grant.Role,
		})
	}
	return g.members.ReplaceTaskGrantsTx(ctx, tx, taskID, writes, by)
}

func (g taskGrants) Names(ctx context.Context, persons, teams []string) (map[string]string, map[string]string, error) {
	hash, err := actorTokenHash(ctx)
	if err != nil {
		return nil, nil, err
	}
	return g.members.NamesOf(ctx, hash, persons, teams)
}

func (g taskGrants) TeamsOf(ctx context.Context, person string) ([]string, error) {
	id, err := identity.ParseUserID(person)
	if err != nil {
		return nil, err
	}
	return g.members.TeamsOf(ctx, id)
}
