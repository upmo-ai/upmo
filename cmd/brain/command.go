package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/backup"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
)

func command(name string, args []string) error {
	switch name {
	case "migrate":
		return migrateCommand()
	case "backup":
		return backupCommand(args, os.Stdout)
	case "restore":
		return restoreCommand(args, os.Stdout)
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
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	passkeys, err := identity.Passkeys(pool, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		return err
	}
	switch name {
	case "bootstrap":
		return bootstrap(ctx, passkeys, args)
	case "recover":
		return recover(ctx, passkeys, identity.Members(pool, cfg.PublicURL, brainops.LinkMail(cfg.SMTP), log), args)
	default:
		return fmt.Errorf("brain: unknown command %q", name)
	}
}

func migrateCommand() error {
	dbURL := strings.TrimSpace(os.Getenv("UPMO_DATABASE_URL"))
	if dbURL == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		dbURL = cfg.DatabaseURL
	} else if err := tlscheck.PostgresURL("UPMO_DATABASE_URL", dbURL); err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
}

func loadBackupEnv() (backupEnv, error) {
	dbURL := strings.TrimSpace(os.Getenv("UPMO_DATABASE_URL"))
	if dbURL == "" {
		return backupEnv{}, errors.New("config: UPMO_DATABASE_URL is required")
	}
	if err := tlscheck.PostgresURL("UPMO_DATABASE_URL", dbURL); err != nil {
		return backupEnv{}, err
	}
	artDir := strings.TrimSpace(os.Getenv("UPMO_ARTIFACTS_DIR"))
	if artDir != "" && (strings.ContainsAny(artDir, "\x00") || !strings.HasPrefix(artDir, "/")) {
		return backupEnv{}, errors.New("config: UPMO_ARTIFACTS_DIR must be an absolute path")
	}
	knowDir := strings.TrimSpace(os.Getenv("UPMO_KNOWLEDGE_DIR"))
	if knowDir != "" && (strings.ContainsAny(knowDir, "\x00") || !strings.HasPrefix(knowDir, "/")) {
		return backupEnv{}, errors.New("config: UPMO_KNOWLEDGE_DIR must be an absolute path")
	}
	return backupEnv{
		databaseURL:  dbURL,
		artifactsDir: artDir,
		knowledgeDir: knowDir,
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
	noEncrypt := fs.Bool("no-encrypt", false, "write an unencrypted tar.gz archive")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outPath == "" {
		return errors.New("brain: backup requires --out")
	}
	env, err := loadBackupEnv()
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
	env, err := loadBackupEnv()
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
