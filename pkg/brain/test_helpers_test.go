//go:build integration

package brain

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

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
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
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
	return routesAt(nil, pool, cfg, log, boxes, work, noticeStore, streams, chatStreams, schemaVersions, reg, opts...)
}

// routesAt is routes with the identity clock pinned to now; nil keeps time.Now.
func routesAt(
	now func() time.Time,
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
	passwords := identity.Passwords(pool, brainops.NewEmailSealer(masterKeys), sessions)
	// No pkg/brain test signs in or sets a password here: Keys stays unset,
	// which refuses every password call (the identity suites drive the worker).
	tokens := identity.APITokens(pool, log)
	passkeys, err := identity.Passkeys(pool, brainops.NewEmailSealer(masterKeys), sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		return nil, err
	}
	if now != nil {
		sessions.Now = now
		passwords.Now = now
		passkeys.Now = now
		passkeys.Sessions = sessions
		passwords.Sessions = sessions
	}
	members := identity.WithAvatars(identity.Members(pool, brainops.NewEmailSealer(masterKeys), cfg.PublicURL, brainops.LinkMail(cfg.Env, cfg.SMTP), log), cfg.AvatarsDir)
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
	return buildHandler(deps, ideps, boxes, work, streams, chatStreams, o)
}

// servingPools injects the two pools brain runs on as the roles brain logs in
// as in production: a real upmo_app login and a real upmo_runner login. A
// superuser session that switched role would fail brain's role check.
// withWorker starts the worker's real key service over owner with the ring
// the environment names, and points cfg at it as brain's key service.
func withWorker(t *testing.T, owner *pgxpool.Pool, cfg *Config) *workerkeys.Worker {
	t.Helper()
	ring, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	w := workerkeys.Start(t, owner, ring)
	cfg.WorkerURL = w.URL
	cfg.WorkerBrainKey = w.Key[svcsig.AudienceWorkerBrain]
	return w
}

func servingPools(t *testing.T, owner *pgxpool.Pool) Option {
	t.Helper()
	return WithPools(pgtest.AppLogin(t, owner), pgtest.Login(t, owner, "upmo_runner"))
}
