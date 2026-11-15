package identity

import (
	"log/slog"
	"net"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/app"
)

// PasskeyService is the passkey use case. Commands use this name so they do not import the app package.
type PasskeyService = app.Passkeys

// Passkeys builds the passkey use case.
func Passkeys(pool *pgxpool.Pool, sessions app.Service, publicURL, rpID string, log *slog.Logger) (app.Passkeys, error) {
	ceremony, err := webauthn.New(rpID, publicURL)
	if err != nil {
		return app.Passkeys{}, err
	}
	return app.Passkeys{
		Sessions:  sessions,
		Store:     postgres.Passkeys{Pool: pool},
		Ceremony:  ceremony,
		Log:       log,
		PublicURL: publicURL,
	}, nil
}

// PasswordSealer seals and opens a stored password hash. The brain wires it
// from the master keyring.
type PasswordSealer = app.PasswordSealer

var (
	// ErrNoSealer means no master key is wired, so no password hash can be stored.
	ErrNoSealer = app.ErrNoSealer
	// PasswordRef is the seal ref of one person's stored password hash.
	PasswordRef = app.PasswordRef
)

// Passwords builds the password use case on the same pool as the passkeys.
// The caller sets the returned value's Sealer from the master keyring.
func Passwords(pool *pgxpool.Pool, sessions app.Service) app.Passwords {
	return app.Passwords{Sessions: sessions, Store: postgres.Passkeys{Pool: pool}}
}

// MountPasskeys registers the passkey and password routes.
func MountPasskeys(mux *http.ServeMux, passkeys app.Passkeys, passwords app.Passwords, trusted []*net.IPNet) {
	(httpapi.Passkeys{Pass: passkeys, Password: passwords, Trusted: trusted}).Mount(mux)
}
