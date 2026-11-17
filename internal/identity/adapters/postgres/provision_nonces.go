package postgres

import (
	"context"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// ConsumeNonce records one v2 provision bearer nonce for the key that
// verified it, so a second request with the same bearer is a replay. A nonce
// already recorded for that key writes nothing and reports ErrNonceReplay.
func (s ProvisionStore) ConsumeNonce(ctx context.Context, keyID, nonce string) error {
	tag, err := s.Pool.Exec(ctx, `
		INSERT INTO identity.provision_nonces (key_id, nonce, seen_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key_id, nonce) DO NOTHING`, keyID, nonce, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNonceReplay
	}
	return nil
}

// SweepProvisionNonces deletes consumed nonces older than the replay window
// (svcsig.NonceRetention) before now and returns how many went. Rows inside
// the window survive, so a bearer still inside its timestamp tolerance can
// never slip a swept nonce past the replay check.
func (s ProvisionStore) SweepProvisionNonces(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
		DELETE FROM identity.provision_nonces
		WHERE seen_at < $1`, now.Add(-svcsig.NonceRetention))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
