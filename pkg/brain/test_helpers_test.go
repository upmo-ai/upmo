//go:build integration

package brain

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks"
)

func routes(
	pool *pgxpool.Pool,
	cfg config.Config,
	log *slog.Logger,
	boxes *runtime.TaskSandboxes,
	work *jobs.Client,
	noticeStore notices.Store,
	streams *tasks.StreamHub,
	chatStreams *chat.StreamHub,
	schemaVersions func(context.Context) (map[string]int64, error),
	reg *metrics.Registry,
	opts ...Option,
) (http.Handler, error) {
	masterKeys, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		return nil, err
	}
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	passwords := identity.Passwords(pool, sessions)
	passwords.Sealer = brainops.NewPasswordSealer(masterKeys)
	tokens := identity.APITokens(pool, log)
	passkeys, err := identity.Passkeys(pool, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		return nil, err
	}
	members := identity.Members(pool, cfg.PublicURL, brainops.LinkMail(cfg.SMTP), log)
	providers := gateway.NewProviders(pool, masterKeys, log)
	providers.LocalPlan = func(ctx context.Context, user string) bool {
		id, err := identity.ParseUserID(user)
		if err != nil {
			return false
		}
		allow, err := members.SettingsAllow(ctx, id)
		return err == nil && allow != ""
	}
	providers.Allow = func(ctx context.Context, user string) (string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return "", err
		}
		return members.SettingsAllow(ctx, person)
	}
	providers.Teams = func(ctx context.Context, user string) ([]string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return nil, err
		}
		return members.TeamsOf(ctx, person)
	}
	providers.Names = func(ctx context.Context, teamIDs []string) (map[string]string, error) {
		_, names, err := members.NamesOf(ctx, nil, teamIDs)
		return names, err
	}
	routesSvc := gateway.NewRoutes(pool, log)
	routesSvc.Allow = providers.Allow
	routesSvc.Teams = providers.Teams
	routesSvc.Names = providers.Names

	idFacade := identity.New(sessions, passkeys, passwords)
	pubBuild, rawBuild := resolveBuildInfo()

	deps := Deps{
		App:            pool,
		Identity:       identityAdapter{id: idFacade},
		Members:        memberAdapter{members: members},
		Notices:        noticeStore,
		Jobs:           work,
		Sealer:         sealerAdapter{keys: masterKeys},
		Logger:         log,
		Config:         fromInternalConfig(cfg),
		BuildInfo:      pubBuild,
		SchemaVersions: schemaVersions,
	}
	ideps := internalDeps{
		identity:  idFacade,
		members:   members,
		roles:     identity.Roles(pool, log),
		teams:     identity.Teams(pool, log),
		tokens:    tokens,
		workspace: identity.Workspace(pool),
		providers: providers,
		routes:    routesSvc,
		routeBook: gateway.NewRouteBook(pool),
		keyring:   masterKeys,
		agents:    agents.NewStore(pool),
		notices:   noticeStore,
		metrics:   reg,
		buildRaw:  rawBuild,
	}
	if buildinfo.Info().Version == "" {
		ideps.buildRaw = buildinfo.Details{}
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return buildHandler(deps, ideps, boxes, work, streams, chatStreams, nil, o)
}
