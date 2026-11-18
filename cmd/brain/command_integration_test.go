//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// The link-issued events name the event alone: who got the link is already in
// the audit trail, and the log must not carry the address.
func TestLinkIssuedLogsCarryNoEmailAddress(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	for schema, files := range map[string]fs.FS{audit.Schema: audit.Migrations(), identity.Schema: identity.Migrations()} {
		if err := migrate.Up(ctx, owner, schema, files); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(owner, identity.IdleTimeout, identity.MaxAge)
	passkeys, err := identity.Passkeys(owner, sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	members := identity.Members(owner, "http://localhost:5173", brainops.LinkMail(config.SMTP{}), log)

	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if err := bootstrap(ctx, passkeys, []string{"--email", "founder@example.com", "--name", "Founder"}); err != nil {
		t.Fatal(err)
	}
	if err := recover(ctx, passkeys, members, []string{"--email", "founder@example.com"}); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	if !strings.Contains(out, "bootstrap link issued") || !strings.Contains(out, "recover link issued") {
		t.Fatalf("log = %s, want both link-issued events", out)
	}
	if strings.Contains(out, "founder@example.com") {
		t.Fatalf("log = %s, want no address", out)
	}
}

func TestMigrateCommandAppliesAllSchemasAndAgents(t *testing.T) {
	t.Setenv("UPMO_ENV", "development")
	owner := pgtest.New(t)
	u, err := url.Parse(owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + owner.Config().ConnConfig.Database
	t.Setenv("UPMO_DATABASE_URL", u.String())
	if err := command("migrate", nil); err != nil {
		t.Fatalf("command(migrate) = %v", err)
	}
	var reg *string
	if err := owner.QueryRow(context.Background(), `SELECT to_regclass('agents.agents')::text`).Scan(&reg); err != nil {
		t.Fatal(err)
	}
	if reg == nil {
		t.Fatal("agents.agents was not created by migrate")
	}
	var gwBundles *string
	if err := owner.QueryRow(context.Background(), `SELECT to_regclass('gateway.agent_bundles')::text`).Scan(&gwBundles); err != nil {
		t.Fatal(err)
	}
	if gwBundles != nil {
		t.Fatal("gateway.agent_bundles still exists after migrate")
	}
}
