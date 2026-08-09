package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
)

func command(name string, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		return err
	}
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	passkeys, err := identity.Passkeys(pool, sessions, cfg.PublicURL, cfg.WebAuthnRPID, slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err != nil {
		return err
	}
	switch name {
	case "bootstrap":
		return bootstrap(ctx, passkeys, args)
	case "recover":
		return recover(ctx, passkeys, identity.Members(pool), args)
	default:
		return fmt.Errorf("brain: unknown command %q", name)
	}
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
	slog.Info("bootstrap link issued", "email", *email)
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
	slog.Info("recover link issued", "email", *email)
	return nil
}
