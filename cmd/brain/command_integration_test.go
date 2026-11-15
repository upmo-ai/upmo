//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
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
	members := identity.Members(owner, "http://localhost:5173", linkMail(config.SMTP{}), log)

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
