package gateway

import "github.com/upmo-ai/upmo/internal/platform/secretchain"

// The secret chain lives in internal/platform/secretchain so the brain
// resolves credentials the same way as the gateway. These aliases keep the
// gateway's names working; DBSecrets stays in keyring.go.

// ErrMissing is a credential the backend does not hold.
var ErrMissing = secretchain.ErrMissing

// VaultHTTP reads a KV v2 secret data.value or data for the ref key.
var VaultHTTP = secretchain.VaultHTTP

// SecretBackend is one enabled secret source.
type SecretBackend = secretchain.Backend

// Chain tries each enabled backend until one returns a value.
type Chain = secretchain.Chain

// EnvSecrets reads UPMO_CREDENTIAL_<ref>. The value is never logged.
type EnvSecrets = secretchain.EnvSecrets

// AWSSecrets resolves a ref through an injected getter.
// A nil Getter means every lookup is missing.
type AWSSecrets = secretchain.AWSSecrets

// VaultSecrets reads a KV path through an injected getter.
// Addr and Token must be set or every lookup is missing.
type VaultSecrets = secretchain.VaultSecrets
