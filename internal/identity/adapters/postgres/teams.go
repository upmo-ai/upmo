package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/identity/app"
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

// AddTeamMember puts a joined person on a team through
// identity.add_team_member for the actor behind actorTokenHash. Adding them
// again changes nothing and records no event. The team.member_add event
// records the added user in the same transaction.
func (p Passkeys) AddTeamMember(ctx context.Context, actorTokenHash []byte, team domain.TeamID, user domain.UserID) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var added bool
	if err := tx.QueryRow(ctx, `SELECT * FROM identity.add_team_member($1, $2, $3)`,
		actorTokenHash, team, user).Scan(&added); err != nil {
		return mapAddTeamMember(err)
	}
	if !added {
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

// mapAddTeamMember maps the add_team_member and remove_team_member refusals
// to their domain errors; any other error is a team write.
func mapAddTeamMember(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Message {
		case "identity: invalid actor session":
			return app.ErrNotSignedIn
		case "identity: only team managers manage teams":
			return domain.ErrTeamsOnly
		case "identity: no such member":
			return domain.ErrNoSuchMember
		case "identity: not joined":
			return domain.ErrNotJoined
		case "identity: no such team":
			return domain.ErrNoSuchTeam
		}
	}
	return mapTeamWrite(err)
}

// RemoveTeamMember takes a person off a team through
// identity.remove_team_member for the actor behind actorTokenHash. Removing
// someone who is not on the team changes nothing and records no event. The
// team.member_remove event records the removed user in the same transaction.
func (p Passkeys) RemoveTeamMember(ctx context.Context, actorTokenHash []byte, team domain.TeamID, user domain.UserID) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var removed bool
	if err := tx.QueryRow(ctx, `SELECT * FROM identity.remove_team_member($1, $2, $3)`,
		actorTokenHash, team, user).Scan(&removed); err != nil {
		return mapAddTeamMember(err)
	}
	if !removed {
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
