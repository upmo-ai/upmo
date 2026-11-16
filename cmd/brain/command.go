package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/artifacts"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks"
)

func migrateAll(ctx context.Context, owner *pgxpool.Pool) error {
	steps := []struct {
		schema string
		files  fs.FS
	}{
		{audit.Schema, audit.Migrations()},
		{identity.Schema, identity.Migrations()},
		{memory.Schema, memory.Migrations()},
		{knowledge.Schema, knowledge.Migrations()},
		{tasks.Schema, tasks.Migrations()},
		{chat.Schema, chat.Migrations()},
		{decisions.Schema, decisions.Migrations()},
		{artifacts.Schema, artifacts.Migrations()},
		{runtime.Schema, runtime.Migrations()},
		{gateway.Schema, gateway.Migrations()},
		{agents.Schema, agents.Migrations()},
	}
	for _, step := range steps {
		if err := migrate.Up(ctx, owner, step.schema, step.files); err != nil {
			return err
		}
	}
	if err := jobs.Up(ctx, owner); err != nil {
		return err
	}
	if err := notices.Up(ctx, owner); err != nil {
		return err
	}
	return audit.Migrated(ctx, owner)
}

func command(name string, args []string) error {
	if name == "migrate" {
		return migrateCommand()
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	// Operator commands keep the owner credential: they migrate and write as
	// the schema owner, and never open a request pool.
	pool, err := openCommandPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrateAll(ctx, pool); err != nil {
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
		return recover(ctx, passkeys, identity.Members(pool, cfg.PublicURL, linkMail(cfg.SMTP), log), args)
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
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrateAll(ctx, pool); err != nil {
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

// openCommandPool opens the owner pool for operator commands without exposing
// UPMO_DATABASE_URL in parse or connect errors.
func openCommandPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, poolURLError("UPMO_DATABASE_URL", databaseURL)
	}
	return pool, nil
}
