//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

// Sandboxd logs in as upmo_sandbox, which is no superuser, not BYPASSRLS and
// not the table owner, so gateway.serving() is false for it. The owner URL
// UPM-406 first proposed, and any other role, is refused at start. With no
// URL there is no pool, and credential reads fail closed.
func TestSandboxdOpensOnlyTheSandboxRole(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool, err := openDatabase(ctx, config.Sandboxd{DatabaseSandboxURL: pgtest.LoginURL(t, owner, "upmo_sandbox")})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var who string
	var serving, bypass bool
	if err := pool.QueryRow(ctx, `
		SELECT current_user, gateway.serving(), rolbypassrls
		FROM pg_roles WHERE rolname = current_user`).Scan(&who, &serving, &bypass); err != nil ||
		who != "upmo_sandbox" || serving || bypass {
		t.Fatalf("current_user=%q serving=%t rolbypassrls=%t %v", who, serving, bypass, err)
	}

	u, err := url.Parse(owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + owner.Config().ConnConfig.Database
	if p, err := openDatabase(ctx, config.Sandboxd{DatabaseSandboxURL: u.String()}); err == nil {
		p.Close()
		t.Fatal("sandboxd opened the owner URL")
	} else if !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("owner URL = %v, want a refusal naming BYPASSRLS", err)
	}
	if p, err := openDatabase(ctx, config.Sandboxd{DatabaseSandboxURL: pgtest.AppLoginURL(t, owner)}); err == nil {
		p.Close()
		t.Fatal("sandboxd opened the app role")
	} else if err.Error() != "brain: UPMO_DATABASE_SANDBOX_URL must log in as upmo_sandbox, not upmo_app" {
		t.Fatalf("app URL = %v", err)
	}
	if p, err := openDatabase(ctx, config.Sandboxd{}); p != nil || err != nil {
		t.Fatalf("no URL = %v %v, want no pool and no error", p, err)
	}
}

// With a real worker the start check passes, signed with sandboxd's own
// worker key; a key the worker does not hold is not ready.
func TestSandboxdStartChecksTheWorker(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	ring, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'1'}, 32)), name == gateway.EnvMasterKey
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker := workerkeys.Start(t, owner, ring)
	cfg := config.Sandboxd{Env: config.EnvDevelopment, WorkerURL: worker.URL, WorkerSandboxdKey: worker.Key[svcsig.AudienceWorkerSandboxd]}
	if c, err := openWorkerKeys(ctx, cfg); err != nil || c == nil {
		t.Fatalf("ready worker = %v %v", c, err)
	}
	cfg.WorkerSandboxdKey = "upmo_svc_worker-sandboxd_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, 32))
	if _, err := openWorkerKeys(ctx, cfg); err == nil || err.Error() != "config: worker is not ready" {
		t.Fatalf("unknown key = %v", err)
	}
	if c, err := openWorkerKeys(ctx, config.Sandboxd{}); c != nil || err != nil {
		t.Fatalf("no worker = %v %v, want no client", c, err)
	}
}
