package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// ListTeams returns every team, ordered by name.
func (p Passkeys) ListTeams(ctx context.Context) ([]domain.Team, error) {
	rows, err := p.Pool.Query(ctx, `SELECT id::text, name FROM identity.teams ORDER BY lower(name), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Team
	for rows.Next() {
		var idText, name string
		if err := rows.Scan(&idText, &name); err != nil {
			return nil, err
		}
		id, err := domain.ParseTeamID(idText)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.Team{ID: id, Name: name})
	}
	return out, rows.Err()
}

// CreateTeam inserts a team and records the team.create event in the same
// transaction.
func (p Passkeys) CreateTeam(ctx context.Context, name string) (domain.TeamID, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return domain.TeamID{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var idText string
	err = tx.QueryRow(ctx, `
		INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&idText)
	if err != nil {
		return domain.TeamID{}, mapTeamWrite(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.TeamCreate,
		Outcome:  audit.Success,
		TargetID: idText,
	}); err != nil {
		return domain.TeamID{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TeamID{}, err
	}
	return domain.ParseTeamID(idText)
}

// AddTeamMember puts a joined person on a team. Adding them again changes
// nothing and records no event. The team.member_add event records the added
// user in the same transaction.
func (p Passkeys) AddTeamMember(ctx context.Context, team domain.TeamID, user domain.UserID) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var joined bool
	err = tx.QueryRow(ctx, `
		SELECT joined_at IS NOT NULL FROM identity.users
		WHERE id = $1 AND disabled_at IS NULL`, user).Scan(&joined)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchMember
	}
	if err != nil {
		return err
	}
	if !joined {
		return domain.ErrNotJoined
	}
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM identity.teams WHERE id = $1`, team).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNoSuchTeam
		}
		return err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, team, user)
	if err != nil {
		return mapTeamWrite(err)
	}
	if tag.RowsAffected() == 0 {
		// Already on the team, so nothing changed and there is no event.
		audit.NoChange(ctx)
		return tx.Commit(ctx)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.TeamMemberAdd,
		Outcome:  audit.Success,
		TargetID: team.String(),
		Changes: audit.Changes{
			Values: map[string]audit.Change{"user_id": {To: user.String()}},
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveTeamMember takes a person off a team. Removing someone who is not on
// the team changes nothing and records no event. The team.member_remove event
// records the removed user in the same transaction.
func (p Passkeys) RemoveTeamMember(ctx context.Context, team domain.TeamID, user domain.UserID) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM identity.teams WHERE id = $1`, team).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNoSuchTeam
		}
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM identity.team_members WHERE team_id = $1 AND user_id = $2`, team, user)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Not on the team, so nothing changed and there is no event.
		audit.NoChange(ctx)
		return tx.Commit(ctx)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.TeamMemberRemove,
		Outcome:  audit.Success,
		TargetID: team.String(),
		Changes: audit.Changes{
			Values: map[string]audit.Change{"user_id": {To: user.String()}},
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func mapTeamWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505":
			return domain.ErrTeamNameTaken
		case pgErr.Code == "23503":
			return domain.ErrNoSuchTeam
		case pgErr.Message == "team member has not joined":
			return domain.ErrNotJoined
		}
	}
	return err
}
