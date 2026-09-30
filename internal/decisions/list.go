package decisions

import (
	"context"

	"github.com/google/uuid"
)

// Lister returns accepted gates.
type Lister interface {
	List(ctx context.Context, access Access) ([]ListedGate, error)
}

// ListGates returns accepted gates for a settings manager.
func ListGates(ctx context.Context, book Lister, access Access) ([]ListedGate, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || !allows(access.Allow, "settings:manage") {
		return nil, ErrAccess
	}
	if book == nil {
		return []ListedGate{}, nil
	}
	out, err := book.List(ctx, access)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []ListedGate{}
	}
	return out, nil
}
