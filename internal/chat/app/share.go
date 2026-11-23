package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// Share is the stored share of one chat: its visibility, the team a team
// visibility names, whether earlier turns read private data, and the members
// with the names the server resolved.
type Share struct {
	Visibility  string
	TeamID      string
	PrivateRead bool
	Members     []ShareMember
}

// ShareMember is one member of a chat share. The owner entry is first.
type ShareMember struct {
	SubjectType string
	SubjectID   string
	Name        string
	Role        string
}

// ChatShare is the chat row behind a share: the owner, the visibility with
// its team, whether identity lets this person edit the chat, and whether
// earlier turns read private data.
type ChatShare struct {
	OwnerID     string
	Visibility  string
	TeamID      string
	Editable    bool
	PrivateRead bool
}

// ShareBook is the chat side of a share. ShareRow answers under row security,
// ErrGone for a chat this person cannot see. SaveShare locks the chat row,
// refuses while a run is active, replaces the grants, stores visibility, and
// seals chat.share in one transaction.
type ShareBook interface {
	ShareRow(ctx context.Context, access Access, chatID string) (ChatShare, error)
	SaveShare(ctx context.Context, access Access, chatID, visibility, teamID string, grants []domain.ShareGrant, grantedBy string) error
}

// ShareGrants is the identity side of a chat share.
type ShareGrants interface {
	ChatGrants(ctx context.Context, chatID string) ([]ShareMember, error)
	ReplaceChatGrants(ctx context.Context, chatID string, grants []domain.ShareGrant, grantedBy string) error
	Names(ctx context.Context, persons, teams []string) (personNames, teamNames map[string]string, err error)
	TeamsOf(ctx context.Context, person string) ([]string, error)
}

// ReadShare returns the stored share of one chat: the owner entry first, then
// the person and team grants with names. The owner and the people who can
// edit the chat may read it; a viewer is refused.
func ReadShare(ctx context.Context, book ShareBook, grants ShareGrants, access Access, chatID string) (Share, error) {
	if err := caller(access); err != nil {
		return Share{}, err
	}
	id, err := uuid.Parse(chatID)
	if err != nil {
		return Share{}, ErrChat
	}
	chatID = id.String()
	row, err := book.ShareRow(ctx, access, chatID)
	if err != nil {
		return Share{}, err
	}
	if !row.Editable && row.OwnerID != access.UserID {
		return Share{}, ErrAccess
	}
	return storedShare(ctx, grants, row, chatID)
}

// WriteShare replaces the share of one chat. Only the owner may write: the
// update is validated, then the store checks for active runs, replaces grants,
// stores visibility, and seals the audit event in one transaction.
func WriteShare(ctx context.Context, book ShareBook, grants ShareGrants, access Access, chatID string, update domain.ShareUpdate) (Share, error) {
	if err := caller(access); err != nil {
		return Share{}, err
	}
	id, err := uuid.Parse(chatID)
	if err != nil {
		return Share{}, ErrChat
	}
	chatID = id.String()
	row, err := book.ShareRow(ctx, access, chatID)
	if err != nil {
		return Share{}, err
	}
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
	if err := book.SaveShare(ctx, access, chatID, update.Visibility, teamID, stored, access.UserID); err != nil {
		return Share{}, err
	}
	return storedShare(ctx, grants, ChatShare{
		OwnerID:     row.OwnerID,
		Visibility:  update.Visibility,
		TeamID:      teamID,
		PrivateRead: row.PrivateRead,
	}, chatID)
}

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

func storedShare(ctx context.Context, grants ShareGrants, row ChatShare, chatID string) (Share, error) {
	members, err := grants.ChatGrants(ctx, chatID)
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
		Visibility:  row.Visibility,
		TeamID:      row.TeamID,
		PrivateRead: row.PrivateRead,
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
