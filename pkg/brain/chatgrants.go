package brain

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/identity"
)

// chatGrants is the identity side of a chat share. The chat context owns the
// port and identity owns the rows, so brain joins them.
type chatGrants struct {
	members identity.MemberService
}

var _ chat.ShareGrants = chatGrants{}

func (g chatGrants) ChatGrants(ctx context.Context, chatID string) ([]chat.ShareMember, error) {
	rows, err := g.members.ChatGrants(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]chat.ShareMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, chat.ShareMember{
			SubjectType: row.SubjectType,
			SubjectID:   row.SubjectID,
			Name:        row.Name,
			Role:        row.Level,
		})
	}
	return out, nil
}

func (g chatGrants) ReplaceChatGrants(ctx context.Context, chatID string, grants []chat.ShareGrant, grantedBy string) error {
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
	return g.members.ReplaceChatGrants(ctx, chatID, writes, by)
}

func (g chatGrants) ReplaceChatGrantsTx(ctx context.Context, tx pgx.Tx, chatID string, grants []chat.ShareGrant, grantedBy string) error {
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
	return g.members.ReplaceChatGrantsTx(ctx, tx, chatID, writes, by)
}

func (g chatGrants) Names(ctx context.Context, persons, teams []string) (map[string]string, map[string]string, error) {
	return g.members.NamesOf(ctx, persons, teams)
}

func (g chatGrants) TeamsOf(ctx context.Context, person string) ([]string, error) {
	id, err := identity.ParseUserID(person)
	if err != nil {
		return nil, err
	}
	return g.members.TeamsOf(ctx, id)
}
