package inject

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	workerkeys "github.com/upmo-ai/upmo/internal/platform/keys"
)

// WorkerResolver reads a sandbox-bound sealed credential and asks the worker
// to unwrap its data key. It never holds the master key.
type WorkerResolver struct {
	DB   *pgxpool.Pool
	Keys *workerkeys.Client
}

// ResolveForSandbox opens one credential only while its sandbox binding is live.
func (r WorkerResolver) ResolveForSandbox(ctx context.Context, sandboxID string, proxySecret []byte, ref string) ([]byte, error) {
	if r.DB == nil || r.Keys == nil || sandboxID == "" || ref == "" || len(proxySecret) != 32 {
		return nil, ErrNoBackend
	}
	hash := sha256.Sum256(proxySecret)
	var ciphertext, nonce []byte
	var keyID string
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := r.DB.QueryRow(dbCtx, `SELECT ciphertext, nonce, key_id FROM gateway.credential_for_sandbox($1, $2, $3)`, sandboxID, ref, hash[:]).Scan(&ciphertext, &nonce, &keyID)
	cancel()
	if err != nil {
		return nil, ErrNoBackend
	}
	kCtx, kCancel := context.WithTimeout(ctx, 2*time.Second)
	dataKey, err := r.Keys.UnwrapForSandbox(kCtx, sandboxID, ref, proxySecret)
	kCancel()
	defer clear(dataKey)
	if err != nil || len(dataKey) != 32 {
		return nil, workerkeys.ErrUnavailable
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, ErrNoBackend
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, ErrNoBackend
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte(ref))
	if err != nil {
		return nil, ErrNoBackend
	}
	if len(plain) == 0 || ctx.Err() != nil {
		clear(plain)
		return nil, ErrNoBackend
	}
	return plain, nil
}
