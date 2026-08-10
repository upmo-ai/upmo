package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/identity/domain"
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

// CreateTeam inserts a team.
func (p Passkeys) CreateTeam(ctx context.Context, name string) (domain.TeamID, error) {
	var idText string
	err := p.Pool.QueryRow(ctx, `
		INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&idText)
	if err != nil {
		return domain.TeamID{}, mapTeamWrite(err)
	}
	return domain.ParseTeamID(idText)
}

// AddTeamMember puts a joined person on a team. Doing it twice changes nothing.
func (p Passkeys) AddTeamMember(ctx context.Context, team domain.TeamID, user domain.UserID) error {
	var joined bool
	err := p.Pool.QueryRow(ctx, `
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
	if err := p.teamExists(ctx, team); err != nil {
		return err
	}
	_, err = p.Pool.Exec(ctx, `
		INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, team, user)
	return mapTeamWrite(err)
}

// RemoveTeamMember takes a person off a team.
func (p Passkeys) RemoveTeamMember(ctx context.Context, team domain.TeamID, user domain.UserID) error {
	if err := p.teamExists(ctx, team); err != nil {
		return err
	}
	_, err := p.Pool.Exec(ctx, `DELETE FROM identity.team_members WHERE team_id = $1 AND user_id = $2`, team, user)
	return err
}

func (p Passkeys) teamExists(ctx context.Context, team domain.TeamID) error {
	var one int
	err := p.Pool.QueryRow(ctx, `SELECT 1 FROM identity.teams WHERE id = $1`, team).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchTeam
	}
	return err
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
