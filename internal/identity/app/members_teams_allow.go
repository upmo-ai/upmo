package app

import (
	"context"
	"errors"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// TeamsAllow is teams:manage when the person holds it, otherwise empty. A
// person who is not a joined, enabled member holds nothing.
func (m Members) TeamsAllow(ctx context.Context, id domain.UserID) (string, error) {
	subject, err := m.actor(ctx, id)
	if errors.Is(err, domain.ErrNoSuchMember) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, permission := range subject.Permissions {
		if permission == domain.PermTeamsManage {
			return string(domain.PermTeamsManage), nil
		}
	}
	return "", nil
}
