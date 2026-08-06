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

// MountPasskeys registers the passkey routes.
func MountPasskeys(mux *http.ServeMux, passkeys app.Passkeys, trusted []*net.IPNet) {
	(httpapi.Passkeys{Pass: passkeys, Trusted: trusted}).Mount(mux)
}
