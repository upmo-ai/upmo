package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/backup"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/platform/secretchain"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
	"github.com/upmo-ai/upmo/internal/provision"
)

func command(name string, args []string) error {
	switch name {
	case "migrate":
		return migrateCommand()
	case "backup":
		return backupCommand(args, os.Stdout)
	case "restore":
		return restoreCommand(args, os.Stdout)
	case "worker":
		return workerCommand()
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	// Operator commands keep the owner credential: they migrate and write as
	// the schema owner, and never open a request pool.
	pool, err := brainops.OpenCommandPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := brainops.MigrateAll(ctx, pool); err != nil {
		return err
	}
	ring, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		return err
	}
	emails := brainops.NewEmailSealer(ring)
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	passkeys, err := identity.Passkeys(pool, emails, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		return err
	}
	switch name {
	case "keys":
		// A reseal stops cleanly between batches, so Ctrl-C leaves the
		// committed batches in place and the command can be run again.
		keysCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return keysCommand(keysCtx, pool, ring, args, os.Stdout)
	case "bootstrap":
		return bootstrap(ctx, passkeys, args)
	case "recover":
		return recover(ctx, passkeys, identity.Members(pool, emails, cfg.PublicURL, brainops.LinkMail(cfg.Env, cfg.SMTP), log), args)
	default:
		return fmt.Errorf("brain: unknown command %q", name)
	}
}

// checkEnvGuard refuses the development keys and passwords compose.yml embeds
// unless UPMO_ENV is development. migrate, backup and restore read
// UPMO_DATABASE_URL directly instead of through config.Load, so the guard is
// theirs; it runs after the URL checks and before any pool or keyring opens.
func checkEnvGuard(dbURL string) error {
	env, err := config.ParseEnv(os.Getenv("UPMO_ENV"))
	if err != nil {
		return err
	}
	return config.CheckGuard(config.Guard{
		Env:          env,
		PublicURL:    os.Getenv("UPMO_PUBLIC_URL"),
		DatabaseURLs: []config.Setting{{Name: "UPMO_DATABASE_URL", Value: dbURL}},
	}, os.LookupEnv, os.Environ())
}

// developmentEnv reports whether UPMO_ENV is development. A value ParseEnv
// refuses is production here; checkEnvGuard reports it next.
func developmentEnv() bool {
	env, err := config.ParseEnv(os.Getenv("UPMO_ENV"))
	return err == nil && env == config.EnvDevelopment
}

func migrateCommand() error {
	dbURL := strings.TrimSpace(os.Getenv("UPMO_DATABASE_URL"))
	if dbURL == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		dbURL = cfg.DatabaseURL
	} else {
		if err := tlscheck.PostgresURL("UPMO_DATABASE_URL", dbURL, developmentEnv()); err != nil {
			return err
		}
		if err := checkEnvGuard(dbURL); err != nil {
			return err
		}
	}
	ctx := context.Background()
	pool, err := brainops.OpenCommandPool(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := brainops.MigrateAll(ctx, pool); err != nil {
		return err
	}
	slog.Info("migrations applied")
	return nil
}

// keysCommand runs the key-management subcommands. reseal moves every sealed
// row under the current master key; rows whose key is not loaded and rows a
// loaded key cannot open are left alone and make it fail, so an operator sees
// them.
func keysCommand(ctx context.Context, pool *pgxpool.Pool, ring gateway.Keyring, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "reseal" {
		return errors.New("brain: keys needs a subcommand: reseal")
	}
	fs := flag.NewFlagSet("keys reseal", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "report what a reseal would change without changing anything")
	batchSize := fs.Int("batch-size", 200, "rows resealed per batch")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *batchSize < 1 || *batchSize > 10000 {
		return errors.New("brain: keys reseal --batch-size must be between 1 and 10000")
	}
	if !ring.Ready() {
		return errors.New("brain: keys reseal needs UPMO_MASTER_KEY")
	}
	result, err := brainops.Reseal(ctx, pool, ring, brainops.ResealOptions{
		DryRun:    *dryRun,
		BatchSize: *batchSize,
		Out:       stdout,
	})
	if err != nil {
		return err
	}
	var resealed, unknown, failed int64
	ids := map[string]bool{}
	for _, count := range result.Stores {
		resealed += count.Resealed
		unknown += count.Unknown
		failed += count.Failed
		for _, id := range count.UnknownKeyIDs {
			ids[id] = true
		}
	}
	slog.Info("keys reseal finished",
		"key_id", ring.CurrentID(), "dry_run", *dryRun,
		"resealed", resealed, "unknown_key", unknown, "failed", failed)
	if left := unknown + failed; left > 0 {
		unknownIDs := make([]string, 0, len(ids))
		for id := range ids {
			unknownIDs = append(unknownIDs, id)
		}
		sort.Strings(unknownIDs)
		var parts []string
		if unknown > 0 {
			parts = append(parts, fmt.Sprintf("%d under key ids that are not loaded (%s)",
				unknown, strings.Join(unknownIDs, ",")))
		}
		if failed > 0 {
			parts = append(parts, fmt.Sprintf("%d that could not be opened", failed))
		}
		return fmt.Errorf("brain: keys reseal left %d rows unchanged: %s",
			left, strings.Join(parts, ", "))
	}
	return nil
}

func bootstrap(ctx context.Context, passkeys identity.PasskeyService, args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	email := fs.String("email", "", "email of the first owner")
	name := fs.String("name", "", "name of the first owner")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *name == "" {
		return errors.New("brain: bootstrap requires --email and --name")
	}
	token, err := passkeys.Bootstrap(ctx, *email, *name)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(os.Stdout, passkeys.LinkURL(token)); err != nil {
		return err
	}
	slog.Info("bootstrap link issued")
	return nil
}

func recover(ctx context.Context, passkeys identity.PasskeyService, members identity.MemberService, args []string) error {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	email := fs.String("email", "", "email of the person")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("brain: recover requires --email")
	}
	token, err := members.Recover(ctx, *email)
	if errors.Is(err, identity.ErrNoSuchMember) {
		return errors.New("brain: no enabled person with that email")
	}
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(os.Stdout, passkeys.LinkURL(token)); err != nil {
		return err
	}
	slog.Info("recover link issued")
	return nil
}

type backupEnv struct {
	databaseURL  string
	artifactsDir string
	knowledgeDir string
	avatarsDir   string
}

func loadBackupEnv(forBackup bool) (backupEnv, error) {
	dbURL := strings.TrimSpace(os.Getenv("UPMO_DATABASE_URL"))
	if dbURL == "" {
		return backupEnv{}, errors.New("config: UPMO_DATABASE_URL is required")
	}
	if err := tlscheck.PostgresURL("UPMO_DATABASE_URL", dbURL, developmentEnv()); err != nil {
		return backupEnv{}, err
	}
	if err := checkEnvGuard(dbURL); err != nil {
		return backupEnv{}, err
	}
	artDir, err := config.DirFromEnv(os.Getenv("UPMO_ARTIFACTS_DIR"), config.DefaultArtifactsDir)
	if err != nil {
		return backupEnv{}, fmt.Errorf("config: UPMO_ARTIFACTS_DIR %w", err)
	}
	knowDir, err := config.DirFromEnv(os.Getenv("UPMO_KNOWLEDGE_DIR"), config.DefaultKnowledgeDir)
	if err != nil {
		return backupEnv{}, fmt.Errorf("config: UPMO_KNOWLEDGE_DIR %w", err)
	}
	avatarsDir, err := config.DirFromEnv(os.Getenv("UPMO_AVATARS_DIR"), config.DefaultAvatarsDir)
	if err != nil {
		return backupEnv{}, fmt.Errorf("config: UPMO_AVATARS_DIR %w", err)
	}
	if forBackup {
		if strings.TrimSpace(os.Getenv("UPMO_ARTIFACTS_DIR")) == "" {
			if _, err := os.Stat(config.DefaultArtifactsDir); errors.Is(err, os.ErrNotExist) {
				artDir = ""
			}
		}
		if strings.TrimSpace(os.Getenv("UPMO_KNOWLEDGE_DIR")) == "" {
			if _, err := os.Stat(config.DefaultKnowledgeDir); errors.Is(err, os.ErrNotExist) {
				knowDir = ""
			}
		}
		if strings.TrimSpace(os.Getenv("UPMO_AVATARS_DIR")) == "" {
			if _, err := os.Stat(config.DefaultAvatarsDir); errors.Is(err, os.ErrNotExist) {
				avatarsDir = ""
			}
		}
	}
	return backupEnv{
		databaseURL:  dbURL,
		artifactsDir: artDir,
		knowledgeDir: knowDir,
		avatarsDir:   avatarsDir,
	}, nil
}

type backupKeyringAdapter struct {
	ring      gateway.Keyring
	currentID string
	knownIDs  map[string]bool
}

func (a backupKeyringAdapter) Ready() bool { return a.ring.Ready() }

func (a backupKeyringAdapter) CurrentID() string { return a.currentID }

func (a backupKeyringAdapter) HasKey(id string) bool { return a.knownIDs[id] }

func (a backupKeyringAdapter) Seal(ref, plaintext string) (backup.Sealed, error) {
	s, err := a.ring.Seal(ref, plaintext)
	if err != nil {
		return backup.Sealed{}, err
	}
	return backup.Sealed{
		Ciphertext: s.Ciphertext,
		Nonce:      s.Nonce,
		KeyID:      s.KeyID,
	}, nil
}

func (a backupKeyringAdapter) Open(ref string, s backup.Sealed) (string, error) {
	return a.ring.Open(ref, gateway.Sealed{
		Ciphertext: s.Ciphertext,
		Nonce:      s.Nonce,
		KeyID:      s.KeyID,
	})
}

func loadBackupKeyring() (backup.Keyring, error) {
	ring, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		return nil, err
	}
	if !ring.Ready() {
		return backupKeyringAdapter{}, nil
	}
	currentID := strings.TrimSpace(os.Getenv(gateway.EnvMasterKeyID))
	if currentID == "" {
		currentID = "k1"
	}
	known := map[string]bool{currentID: true}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if prevID, ok := strings.CutPrefix(name, gateway.EnvMasterKeyPrevious); ok {
			known[prevID] = true
		}
	}
	return backupKeyringAdapter{
		ring:      ring,
		currentID: currentID,
		knownIDs:  known,
	}, nil
}

func readPassphraseEnv(envName string) (string, error) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return "", nil
	}
	val, ok := os.LookupEnv(envName)
	if !ok || val == "" {
		return "", fmt.Errorf("brain: passphrase environment variable %s is unset or empty", envName)
	}
	return val, nil
}

func backupCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	outPath := fs.String("out", "", "path to write the backup archive")
	passEnv := fs.String("passphrase-env", "", "optional environment variable holding an archive passphrase")
	noEncrypt := fs.Bool("no-encrypt", false, "write an unencrypted tar.gz archive (development only)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	*outPath = strings.TrimSpace(*outPath)
	if *outPath == "" {
		*outPath = "upmo-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	}
	if *noEncrypt {
		// Checked before the env guard and before any pool or keyring opens, so
		// a production backup never stages plaintext and nothing else fails first.
		env, err := config.ParseEnv(os.Getenv("UPMO_ENV"))
		if err != nil {
			return err
		}
		if env == config.EnvProduction {
			return errors.New("brain: backup --no-encrypt is refused when UPMO_ENV=production")
		}
	}
	env, err := loadBackupEnv(true)
	if err != nil {
		return err
	}
	ring, err := loadBackupKeyring()
	if err != nil {
		return err
	}
	passphrase, err := readPassphraseEnv(*passEnv)
	if err != nil {
		return err
	}
	ctx := context.Background()
	owner, err := brainops.OpenOwnerPool(ctx, env.databaseURL)
	if err != nil {
		return err
	}
	defer owner.Close()

	res, err := backup.Backup(ctx, owner, backup.BackupOptions{
		OutPath:      *outPath,
		ArtifactsDir: env.artifactsDir,
		KnowledgeDir: env.knowledgeDir,
		AvatarsDir:   env.avatarsDir,
		Keyring:      ring,
		Passphrase:   passphrase,
		NoEncrypt:    *noEncrypt,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "backup complete: %s (%d tables, %d volume files, master_key_id=%s, encrypted=%t)\n",
		res.OutPath, res.TablesDumped, res.VolumeFiles, res.MasterKeyID, res.Encrypted)
	return err
}

func restoreCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	inPath := fs.String("in", "", "path to the backup archive")
	passEnv := fs.String("passphrase-env", "", "optional environment variable holding the archive passphrase")
	requireEncrypted := fs.Bool("require-encrypted", false, "refuse unencrypted backup archives")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inPath == "" {
		return errors.New("brain: restore requires --in")
	}
	env, err := loadBackupEnv(false)
	if err != nil {
		return err
	}
	ring, err := loadBackupKeyring()
	if err != nil {
		return err
	}
	passphrase, err := readPassphraseEnv(*passEnv)
	if err != nil {
		return err
	}
	supportedVersions, err := brainops.SupportedSchemaVersions()
	if err != nil {
		return err
	}
	ctx := context.Background()
	owner, err := brainops.OpenOwnerPool(ctx, env.databaseURL)
	if err != nil {
		return err
	}
	defer owner.Close()

	res, err := backup.Restore(ctx, owner, backup.RestoreOptions{
		InPath:            *inPath,
		ArtifactsDir:      env.artifactsDir,
		KnowledgeDir:      env.knowledgeDir,
		AvatarsDir:        env.avatarsDir,
		Keyring:           ring,
		Passphrase:        passphrase,
		RequireEncrypted:  *requireEncrypted,
		SupportedVersions: supportedVersions,
		PrepareSchemas:    brainops.PrepareSchemasAt,
		MigrateForward:    brainops.MigrateAll,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "restore complete: %d tables, %d volume files; audit chain valid (head position %d)\n",
		res.TablesRestored, res.VolumeFilesRestored, res.AuditHeadPosition); err != nil {
		return err
	}
	for _, name := range res.VolumesUnchanged {
		if _, err := fmt.Fprintf(stdout, "%s volume left unchanged: archive has no entries for it\n", name); err != nil {
			return err
		}
	}
	return nil
}

// servingRoles are the roles the worker gives a password, each from its own
// URL. A role whose URL is unset stays NOLOGIN.
var servingRoles = []struct{ env, role string }{
	{"UPMO_DATABASE_APP_URL", "upmo_app"},
	{"UPMO_DATABASE_RUNNER_URL", "upmo_runner"},
	{"UPMO_DATABASE_GATEWAY_URL", "upmo_gateway"},
	{"UPMO_DATABASE_SANDBOX_URL", "upmo_sandbox"},
}

// workerCommand runs `upmo worker`, the one process that holds the privileged
// role. It migrates, gives each serving role its password, runs the jobs that
// read or move every person's rows, and serves /api/v1/provision/* on its
// internal listener. Brain, gateway and sandboxd start after it and never
// hold this role.
func workerCommand() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	provisionKeys, err := provision.ParseKeys(cfg.ProvisionKeys)
	if err != nil {
		return err
	}
	// A plaintext Vault address with a token fails the start, however the
	// audit push is configured.
	secrets, err := secretchain.FromEnv(os.LookupEnv)
	if err != nil {
		return err
	}
	if _, err := workerVerifyKeys(cfg); err != nil {
		return err
	}
	ring, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		return err
	}

	owner, err := brainops.OpenOwnerPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer owner.Close()
	if err := backup.CheckPrivileges(ctx, owner); err != nil {
		return err
	}
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		return err
	}
	logins := map[string]string{
		"UPMO_DATABASE_APP_URL":     cfg.DatabaseAppURL,
		"UPMO_DATABASE_RUNNER_URL":  cfg.DatabaseRunnerURL,
		"UPMO_DATABASE_GATEWAY_URL": cfg.DatabaseGatewayURL,
		"UPMO_DATABASE_SANDBOX_URL": cfg.DatabaseSandboxURL,
	}
	for _, r := range servingRoles {
		if logins[r.env] == "" {
			continue
		}
		if err := brainops.EnableLogin(ctx, owner, r.env, r.role, logins[r.env]); err != nil {
			return err
		}
	}

	reg := checkSealedKeys(ctx, owner, ring, cfg.MetricsAddr != "", log)

	work, err := openWorkerJobs(owner, cfg, secrets, ring, reg, log)
	if err != nil {
		return err
	}
	if err := work.Start(ctx); err != nil {
		return err
	}
	defer func() {
		wait, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := work.Stop(wait); err != nil {
			log.Warn("worker jobs still running at shutdown", "err", err)
		}
	}()

	handler, err := workerHandler(owner, cfg, ring, provisionKeys, log)
	if err != nil {
		return err
	}
	log.Info("worker started", "listen", cfg.WorkerListenAddr)
	servers := []*http.Server{httpserver.New(cfg.WorkerListenAddr, handler)}
	if reg != nil {
		servers = append(servers, metrics.Server(cfg.MetricsAddr, reg, cfg.MetricsToken))
	}
	srvCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			errc <- httpserver.Run(srvCtx, srv, cfg.ShutdownTimeout, log)
			cancel()
		}()
	}
	errs := make([]error, len(servers))
	for i := range servers {
		errs[i] = <-errc
	}
	return errors.Join(errs...)
}

// checkSealedKeys warns once per store whose sealed rows use a master key the
// ring does not hold: those rows can never open. It never stops start-up,
// and a counting error is a warning too. With metrics on it returns the
// registry with the gauge each scrape recounts; otherwise nil.
func checkSealedKeys(ctx context.Context, owner *pgxpool.Pool, ring gateway.Keyring, withMetrics bool, log *slog.Logger) *metrics.Registry {
	if unknown, err := brainops.UnknownKeyRows(ctx, owner, ring); err != nil {
		log.Warn("counting sealed rows by key failed", "err", err)
	} else {
		for _, count := range unknown {
			if count.Unknown > 0 {
				log.Warn("sealed rows use a master key that is not loaded",
					"store", count.Store, "rows", count.Unknown, "key_ids", count.UnknownKeyIDs)
			}
		}
	}
	if !withMetrics {
		return nil
	}
	reg := metrics.NewRegistry()
	brainops.RegisterUnknownKeyMetric(reg, owner, ring)
	return reg
}

// workerHandler is the worker's one HTTP surface: provisioning, with its own
// key check, the key service under /internal/keys/, signed per caller
// audience, both on the owner pool, and the health routes compose waits on.
func workerHandler(owner *pgxpool.Pool, cfg config.Config, ring gateway.Keyring, keys []provision.Key, log *slog.Logger) (http.Handler, error) {
	verify, err := workerVerifyKeys(cfg)
	if err != nil {
		return nil, err
	}
	keyRoutes, err := gateway.KeysHandler(owner, ring, verify, log)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	httpserver.Health(mux, owner.Ping)
	mux.Handle("/internal/keys/", keyRoutes)
	provision.Handler{
		Owner:      owner,
		Identity:   identity.WithProvisionAvatars(identity.Provision(owner, brainops.NewEmailSealer(ring), cfg.PublicURL), cfg.AvatarsDir),
		MasterKeys: ring,
		Keys:       keys,
		Log:        log,
	}.Mount(mux)
	return mux, nil
}

// workerVerifyKeys parses the three worker audience keys, each current key
// then its previous one. The worker refuses to start without all three.
func workerVerifyKeys(cfg config.Config) (gateway.KeysVerify, error) {
	verify := gateway.KeysVerify{}
	for _, k := range []struct{ name, audience, current, previous string }{
		{"UPMO_WORKER_BRAIN_KEY", svcsig.AudienceWorkerBrain, cfg.WorkerBrainKey, cfg.WorkerBrainKeyPrevious},
		{"UPMO_WORKER_GATEWAY_KEY", svcsig.AudienceWorkerGateway, cfg.WorkerGatewayKey, cfg.WorkerGatewayKeyPrevious},
		{"UPMO_WORKER_SANDBOXD_KEY", svcsig.AudienceWorkerSandboxd, cfg.WorkerSandboxdKey, cfg.WorkerSandboxdKeyPrevious},
	} {
		if k.current == "" {
			return nil, fmt.Errorf("config: %s is required", k.name)
		}
		for _, secret := range []string{k.current, k.previous} {
			if secret == "" {
				continue
			}
			key, err := svcsig.ParseAudienceKey(secret, k.audience)
			if err != nil {
				return nil, fmt.Errorf("config: %s: %w", k.name, err)
			}
			verify[k.audience] = append(verify[k.audience], key)
		}
	}
	return verify, nil
}

// openWorkerJobs registers every job that reads or moves rows across people
// on the owner pool, on River tables of its own schema: River elects one
// leader per schema, and only the leader inserts periodic jobs, so sharing
// brain's schema could leave these never scheduled.
func openWorkerJobs(owner *pgxpool.Pool, cfg config.Config, secrets secretchain.Chain, ring gateway.Keyring, reg *metrics.Registry, log *slog.Logger) (*jobs.Client, error) {
	workers := river.NewWorkers()
	// The sealer runs on the owner pool: pending and sealed events are the
	// app role's to insert and read, never to move.
	audit.AddSealer(workers, owner, os.Stdout)
	audit.AddRetention(workers, owner, cfg.AuditRetentionDays)
	audit.AddVerifier(workers, owner)
	workspaceZone := func(ctx context.Context) (string, error) {
		return identity.WorkspaceTimeZone(ctx, owner)
	}
	settingsManagers := func(ctx context.Context) ([]string, error) {
		return identity.SettingsManagers(ctx, owner)
	}
	notify := func(ctx context.Context, tx pgx.Tx, userID, title string) error {
		uid, err := uuid.Parse(userID)
		if err != nil {
			return err
		}
		_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
		return err
	}
	gateway.AddSpendChecker(workers, owner, workspaceZone, reg, log)
	gateway.AddUsageRollup(workers, owner, workspaceZone, log)
	gateway.AddProviderChecker(workers, gateway.ProviderCheckDeps{
		Owner:   owner,
		Secrets: append(slices.Clone(secrets), gateway.DBSecrets{Pool: owner, Keys: ring}),
		AgentModelUses: func(ctx context.Context, provider string, models []string) ([]string, error) {
			return agents.SystemProviderModelUses(ctx, owner, provider, models)
		},
		AutomationModelUses: func(context.Context, string, []string) ([]string, error) {
			return nil, nil
		},
		ActiveUser: func(ctx context.Context, userID string) (bool, error) {
			return identity.ActiveUser(ctx, owner, userID)
		},
		SettingsManagers: settingsManagers,
		Notify:           notify,
		Log:              log,
	})
	gateway.AddCatalogRefresher(workers, gateway.CatalogRefreshDeps{
		Owner:            owner,
		SettingsManagers: settingsManagers,
		Notify:           notify,
		Log:              log,
	})
	emails := brainops.NewEmailSealer(ring)
	provision.AddNonceSweep(workers, identity.Provision(owner, emails, cfg.PublicURL))
	periodic := []*river.PeriodicJob{
		audit.SealPeriodic(), audit.RetentionPeriodic(), audit.VerifyPeriodic(),
		provision.NonceSweepPeriodic(), gateway.SpendCheckPeriodic(),
		gateway.ProviderCheckPeriodic(), gateway.CatalogRefreshPeriodic(),
		gateway.UsageRollupPeriodic(),
	}
	if cfg.AuditWebhookURL != "" || cfg.AuditSyslogAddr != "" {
		audit.AddPusher(workers, owner, audit.PushConfig{
			WebhookURL: cfg.AuditWebhookURL,
			SyslogAddr: cfg.AuditSyslogAddr,
			Secrets:    secrets,
		})
		periodic = append(periodic, audit.PushPeriodic())
	}
	return jobs.Open(owner,
		jobs.WithSchema(jobs.WorkerSchema),
		jobs.WithWorkers(workers),
		jobs.WithPeriodic(periodic...),
		jobs.WithMetrics(reg),
	)
}
