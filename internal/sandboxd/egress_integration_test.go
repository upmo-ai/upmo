//go:build integration

package sandboxd

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/moby/moby/client"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/keyservice"
	gatewaydomain "github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	workerkeys "github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime"
	runtimepg "github.com/upmo-ai/upmo/internal/runtime/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/docker"
	"github.com/upmo-ai/upmo/internal/sandboxd/egress"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// TestEgressTunnelOverDocker starts a Docker sandbox on the internal sandbox
// network and drives the egress proxy from inside it: an allowed CONNECT
// tunnels to the upstream, an unlisted one is refused, and a direct
// connection has no route. The proxy runs in this test, so the test must run
// in a container it can attach to the sandbox network, as make
// test-integration does; anywhere else it skips. The upstream is a local
// echo server standing in for the allowed host, so no outside network is
// needed.
func TestEgressTunnelOverDocker(t *testing.T) {
	host := os.Getenv("UPMO_TEST_DOCKER_HOST")
	if host == "" {
		t.Skip("UPMO_TEST_DOCKER_HOST is not set; make test-integration sets it")
	}
	t.Setenv("DOCKER_HOST", host)
	ctx := context.Background()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	self, _ := os.Hostname()
	if _, err := cli.ContainerInspect(ctx, self, client.ContainerInspectOptions{}); err != nil {
		t.Skipf("the test is not in a container on this engine (%v), so a sandbox cannot reach its proxy", err)
	}
	// The upstream: answers each line with pong: and the line.
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = up.Close() })
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					_, _ = fmt.Fprintf(c, "pong:%s\n", sc.Text())
				}
			}()
		}
	}()

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_, proxyPort, _ := net.SplitHostPort(ln.Addr().String())
	proxyURL := "http://sandboxd:" + proxyPort
	netName := testNetwork()
	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	driver, err := docker.Open(openCtx, docker.Options{Network: netName, ProxyURL: proxyURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	var (
		mu      sync.Mutex
		dialled []string
	)
	p := &egress.Proxy{
		Book:   driver,
		Inject: inject.Injector{Resolver: inject.Refuse{}},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			dialled = append(dialled, address)
			mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, up.Addr().String())
		},
	}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	s := spec()
	s.Image = "debian:bookworm-slim"
	if v := os.Getenv("UPMO_TEST_SANDBOX_IMAGE"); v != "" {
		s.Image = v
	}
	box, err := driver.Create(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Destroy(context.Background(), box.ID) })

	sh := func(line string) string {
		t.Helper()
		var out strings.Builder
		_, err := driver.Exec(ctx, box.ID, runtime.Command{Args: []string{"bash", "-c", line}, Timeout: 30 * time.Second},
			func(o runtime.Output) error {
				out.Write(o.Data)
				return nil
			})
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out.String()
	}

	proxyInspect, err := cli.ContainerInspect(ctx, self, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	proxyIP := proxyInspect.Container.NetworkSettings.Networks["upmo-sbx-"+box.ID].IPAddress.String()
	proxyEnv := strings.TrimSpace(sh(`echo "$HTTPS_PROXY"`))
	proxyParsed, err := url.Parse(proxyEnv)
	if err != nil {
		t.Fatal("proxy URL is invalid")
	}
	password, _ := proxyParsed.User.Password()
	proxySecret, err := base64.RawURLEncoding.DecodeString(password)
	if err != nil || len(proxySecret) != 32 || proxyParsed.User.Username() != "sandbox" || proxyParsed.Host != net.JoinHostPort(proxyIP, proxyPort) {
		clear(proxySecret)
		t.Fatal("proxy URL credentials or endpoint are invalid")
	}
	clear(proxySecret)

	// CONNECT through the proxy named in HTTPS_PROXY, with bash's /dev/tcp,
	// since the base image has no curl.
	connect := func(target string) string {
		return sh(fmt.Sprintf(`set -e
proxy=${HTTPS_PROXY#http://}; auth=$(printf '%%s' "${proxy%%@*}" | base64 -w0); hp=${proxy#*@}
exec 3<>/dev/tcp/${hp%%:*}/${hp##*:}
printf 'CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %%s\r\n\r\n' "$auth" >&3
IFS= read -r status <&3; echo "${status%%$'\r'}"
case "$status" in *" 200 "*) ;; *) exit 0 ;; esac
IFS= read -r _ <&3
printf 'ping\n' >&3
IFS= read -r reply <&3; echo "$reply"`, target, target))
	}

	if got := connect("github.com:443"); got != "HTTP/1.1 200 Connection Established\npong:ping\n" {
		t.Fatalf("allowed CONNECT = %q", got)
	}
	mu.Lock()
	got := append([]string(nil), dialled...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "github.com:443" {
		t.Fatalf("dialled %v", got)
	}
	if got := connect("example.com:443"); !strings.HasPrefix(got, "HTTP/1.1 403") {
		t.Fatalf("unlisted CONNECT = %q", got)
	}
	// No route but the proxy.
	if got := sh(`timeout 5 bash -c 'exec 3<>/dev/tcp/1.1.1.1/443' 2>/dev/null && echo open || echo closed`); strings.TrimSpace(got) != "closed" {
		t.Fatalf("direct connection = %q", got)
	}
}

type tracerBook struct {
	principal egress.Principal
	secret    [32]byte
	source    netip.Addr
}

func (b *tracerBook) Authenticate(source netip.Addr, secret []byte) (egress.Principal, bool) {
	h := sha256.Sum256(secret)
	return b.principal, source == b.source && len(secret) == 32 && subtle.ConstantTimeCompare(h[:], b.secret[:]) == 1
}
func (b *tracerBook) Acquire(id string, token uint64) (func(), context.Context, bool) {
	return func() {}, context.Background(), id == b.principal.ID && token == b.principal.Lease
}

type tracerPasswords struct{}

func (tracerPasswords) ByAddress(context.Context, string) (keyservice.PasswordAccount, error) {
	return keyservice.PasswordAccount{}, nil
}
func (tracerPasswords) BySession(context.Context, []byte) (keyservice.PasswordAccount, bool, error) {
	return keyservice.PasswordAccount{}, false, nil
}
func (tracerPasswords) Take(context.Context, []byte) (int, error) { return 0, nil }
func (tracerPasswords) Check(context.Context, keyservice.PasswordAccount, string) (bool, bool, error) {
	return false, false, nil
}

type tracerEmails struct{}

func (tracerEmails) Own(context.Context, []byte) (keyservice.EmailRow, bool, error) {
	return keyservice.EmailRow{}, false, nil
}
func (tracerEmails) Link(context.Context, []byte) (keyservice.EmailRow, bool, error) {
	return keyservice.EmailRow{}, false, nil
}
func (tracerEmails) Member(context.Context, []byte, string) (keyservice.EmailRow, bool, error) {
	return keyservice.EmailRow{}, false, nil
}
func (tracerEmails) Directory(context.Context, []byte) ([]keyservice.EmailRow, bool, error) {
	return nil, false, nil
}
func (tracerEmails) Open(string, []byte) (string, error) { return "", nil }

// TestSandboxResolvesOneCredentialThroughWorker covers the database binding,
// worker unwrap and verified TLS proxy swap in one request.
func TestSandboxResolvesOneCredentialThroughWorker(t *testing.T) {
	ctx := context.Background()
	owner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	const taskID = "018f3b2c-7a10-7c4a-8e11-111111111111"
	const sandboxID = "sb-tracer-406"
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tasks.tasks (id, owner_user_id, goal, status) VALUES ($1, $2, 'credential tracer', 'Working')`, taskID, "018f3b2c-7a10-7c4a-8e11-111111111112"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	proxySecret := bytes.Repeat([]byte{'s'}, 32)
	secretHash := sha256.Sum256(proxySecret)
	appPool := pgtest.App(t, owner)
	if err := (runtimepg.TaskSandboxes{Pool: appPool}).Claim(ctx, taskID, time.Hour, secretHash[:]); err != nil {
		t.Fatal(err)
	}
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32))
	ring, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		if name == gateway.EnvMasterKey {
			return master, true
		}
		return "", false
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const ref, liveSecret = "github_token", "sk_live_tracer_406"
	wrapped, err := ring.WrapNew(gatewaydomain.KindCredential, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wrapped.DataKey)
	sealed, err := gateway.SealWithDataKey(wrapped, ref, liveSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id, wrapped_key, wrap_nonce) VALUES ($1,$2,$3,$4,$5,$6)`, ref, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.WrappedKey, sealed.WrapNonce); err != nil {
		t.Fatal(err)
	}
	verify := keyservice.Verify{}
	tokens := map[string]string{}
	for _, audience := range []string{svcsig.AudienceWorkerBrain, svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		verify[audience] = [][]byte{raw}
		tokens[audience] = "upmo_svc_" + audience + "_" + base64.RawStdEncoding.EncodeToString(raw)
	}
	workerHandler, err := keyservice.New(owner, ring, verify, func(context.Context, string, []byte, func() error) (workerkeys.ProviderTestResponse, error) {
		return workerkeys.ProviderTestResponse{}, nil
	}, tracerPasswords{}, tracerEmails{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerServer := httptest.NewServer(workerHandler)
	t.Cleanup(workerServer.Close)
	keysClient, err := workerkeys.Open(workerkeys.Options{URL: workerServer.URL, Key: tokens[svcsig.AudienceWorkerSandboxd], Audience: svcsig.AudienceWorkerSandboxd, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	sandboxPool := pgtest.Login(t, owner, "upmo_sandbox")
	var current string
	if err := sandboxPool.QueryRow(ctx, `SELECT current_user`).Scan(&current); err != nil || current != "upmo_sandbox" {
		t.Fatalf("current_user=%q err=%v", current, err)
	}
	var serving bool
	if err := sandboxPool.QueryRow(ctx, `SELECT gateway.serving()`).Scan(&serving); err != nil || serving {
		t.Fatalf("gateway.serving()=%t err=%v", serving, err)
	}
	if _, err := sandboxPool.Exec(ctx, `SELECT ciphertext FROM gateway.credentials WHERE ref=$1`, ref); !pgCodeIs(err, "42501") {
		t.Fatalf("direct credential read=%v, want 42501", err)
	}
	if _, err := sandboxPool.Exec(ctx, `SELECT gateway.attach_sandbox_credential($1,$2,$3)`, taskID, sandboxID, secretHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := sandboxPool.Exec(ctx, `SELECT gateway.bind_sandbox_credential($1,$2,$3)`, sandboxID, ref, secretHash[:]); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+liveSecret {
			t.Errorf("upstream Authorization=%q", got)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)
	rootCAs := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	sentinel := "upmo_sentinel_" + strings.Repeat("a", 64)
	book := &tracerBook{principal: egress.Principal{ID: sandboxID, Allow: []string{"example.com"}, Creds: []runtime.Credential{{Sentinel: sentinel, Ref: ref, Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}}, Lease: 1}, source: netip.MustParseAddr("203.0.113.9")}
	copy(book.secret[:], secretHash[:])
	p := &egress.Proxy{Book: book, Inject: inject.Injector{Sandbox: inject.WorkerResolver{DB: sandboxPool, Keys: &keysClient}}, TLSRootCAs: rootCAs, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, upstream.Listener.Addr().String())
	}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("Authorization", "Bearer "+sentinel)
	authReq := &http.Request{Header: http.Header{}}
	authReq.SetBasicAuth("ignored", base64.RawURLEncoding.EncodeToString(proxySecret))
	req.Header.Set("Proxy-Authorization", authReq.Header.Get("Authorization"))
	_, pass, hasAuth := authReq.BasicAuth()
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(pass)
	if !hasAuth || decodeErr != nil || len(decoded) != 32 {
		t.Fatalf("proxy authentication fixture malformed: auth=%t bytes=%d err=%v", hasAuth, len(decoded), decodeErr)
	}
	if _, ok := book.Authenticate(book.source, decoded); !ok {
		t.Fatal("proxy authentication fixture did not match the book")
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("forward=%d %q", rec.Code, rec.Body.String())
	}
}

func TestEgressContainerCredentialSwap(t *testing.T) {
	host := os.Getenv("UPMO_TEST_DOCKER_HOST")
	if host == "" {
		t.Skip("UPMO_TEST_DOCKER_HOST is not set; make test-integration sets it")
	}
	t.Setenv("DOCKER_HOST", host)
	ctx := context.Background()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	self, _ := os.Hostname()
	if _, err := cli.ContainerInspect(ctx, self, client.ContainerInspectOptions{}); err != nil {
		t.Skipf("the test is not in a container on this engine (%v), so a sandbox cannot reach its proxy", err)
	}
	owner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	const taskID = "018f3b2c-7a10-7c4a-8e11-111111111112"
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tasks.tasks (id, owner_user_id, goal, status) VALUES ($1, $2, 'docker credential swap', 'Working')`, taskID, "018f3b2c-7a10-7c4a-8e11-111111111113"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	proxySecret := bytes.Repeat([]byte{'d'}, 32)
	defer clear(proxySecret)
	secretHash := sha256.Sum256(proxySecret)
	if err := (runtimepg.TaskSandboxes{Pool: pgtest.App(t, owner)}).Claim(ctx, taskID, time.Hour, secretHash[:]); err != nil {
		t.Fatal(err)
	}
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32))
	ring, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		if name == gateway.EnvMasterKey {
			return master, true
		}
		return "", false
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const ref, liveSecret = "github_token", "sk_live_docker_406"
	wrapped, err := ring.WrapNew(gatewaydomain.KindCredential, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wrapped.DataKey)
	sealed, err := gateway.SealWithDataKey(wrapped, ref, liveSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id, wrapped_key, wrap_nonce) VALUES ($1,$2,$3,$4,$5,$6)`, ref, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.WrappedKey, sealed.WrapNonce); err != nil {
		t.Fatal(err)
	}
	verify := keyservice.Verify{}
	tokens := map[string]string{}
	for _, audience := range []string{svcsig.AudienceWorkerBrain, svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		verify[audience] = [][]byte{raw}
		tokens[audience] = "upmo_svc_" + audience + "_" + base64.RawStdEncoding.EncodeToString(raw)
	}
	workerHandler, err := keyservice.New(owner, ring, verify, func(context.Context, string, []byte, func() error) (workerkeys.ProviderTestResponse, error) {
		return workerkeys.ProviderTestResponse{}, nil
	}, tracerPasswords{}, tracerEmails{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerServer := httptest.NewServer(workerHandler)
	t.Cleanup(workerServer.Close)
	keysClient, err := workerkeys.Open(workerkeys.Options{URL: workerServer.URL, Key: tokens[svcsig.AudienceWorkerSandboxd], Audience: svcsig.AudienceWorkerSandboxd, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	sandboxPool := pgtest.Login(t, owner, "upmo_sandbox")
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	var upstreamHits int
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		if got := r.Header.Get("Authorization"); got != "Bearer "+liveSecret {
			t.Errorf("upstream Authorization=%q", got)
		}
		_, _ = io.WriteString(w, liveSecret)
	}))
	t.Cleanup(upstream.Close)
	rootCAs := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	forward := &egress.Proxy{Inject: inject.Injector{Sandbox: inject.WorkerResolver{DB: sandboxPool, Keys: &keysClient}}, TLSRootCAs: rootCAs, Log: logger, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, upstream.Listener.Addr().String())
	}}
	server := &http.Server{Handler: forward, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	driver, err := docker.Open(ctx, docker.Options{DB: sandboxPool, Log: logger, Network: testNetwork(), ProxyURL: "http://sandboxd:" + port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	forward.Book = driver
	sentinel := "upmo_sentinel_" + strings.Repeat("a", 64)
	box, err := driver.Create(ctx, runtime.Spec{Task: taskID, Image: func() string {
		if v := os.Getenv("UPMO_TEST_SANDBOX_IMAGE"); v != "" {
			return v
		}
		return "debian:bookworm-slim"
	}(), Tier: runtime.TierContainer, TTL: time.Minute, Allow: []string{"example.com"}, Credentials: []runtime.Credential{{Sentinel: sentinel, Ref: ref, Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}}, ProxySecret: proxySecret})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Destroy(context.Background(), box.ID) })
	line := `p="${HTTPS_PROXY#http://}"; auth=$(printf '%s' "${p%%@*}" | base64 -w0); hp=${p#*@}; exec 3<>/dev/tcp/${hp%%:*}/${hp##*:}; printf 'GET http://example.com/user HTTP/1.1\r\nHost: example.com\r\nAuthorization: Bearer ` + sentinel + `\r\nProxy-Authorization: Basic %s\r\nConnection: close\r\n\r\n' "$auth" >&3; cat <&3`
	var output strings.Builder
	if _, err := driver.Exec(ctx, box.ID, runtime.Command{Args: []string{"bash", "-c", line}, Timeout: 30 * time.Second}, func(o runtime.Output) error { output.Write(o.Data); return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), sentinel) || strings.Contains(output.String(), liveSecret) || upstreamHits != 1 {
		t.Fatalf("container swap output leaked or missed: hits=%d output=%q", upstreamHits, output.String())
	}
}

func TestSandboxCredentialResolveUnderRLSConcurrency(t *testing.T) {
	ctx := context.Background()
	owner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	proxySecret := bytes.Repeat([]byte{'c'}, 32)
	defer clear(proxySecret)
	secretHash := sha256.Sum256(proxySecret)
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 100; n++ {
		taskID := fmt.Sprintf("018f3b2c-7a10-7c4a-8e11-%012d", n)
		if _, err := tx.Exec(ctx, `INSERT INTO tasks.tasks (id, owner_user_id, goal, status) VALUES ($1, $2, 'RLS concurrency', 'Working')`, taskID, "018f3b2c-7a10-7c4a-8e11-111111111114"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.task_sandboxes (task_id, expires_at, proxy_secret_hash) VALUES ($1, now()+interval '1 hour', $2)`, taskID, secretHash[:]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, query := range []string{"SELECT count(*) FROM tasks.tasks", "SELECT count(*) FROM runtime.task_sandboxes"} {
		if err := owner.QueryRow(ctx, query).Scan(&count); err != nil || count != 100 {
			t.Fatalf("fixture count for %q = %d, err=%v", query, count, err)
		}
	}
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32))
	ring, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		if name == gateway.EnvMasterKey {
			return master, true
		}
		return "", false
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 20)
	live := make([]string, 20)
	for n := range refs {
		refs[n] = fmt.Sprintf("credential_%02d", n)
		live[n] = fmt.Sprintf("sk_live_rls_%02d_406", n)
		wrapped, err := ring.WrapNew(gatewaydomain.KindCredential, refs[n])
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := gateway.SealWithDataKey(wrapped, refs[n], live[n])
		clear(wrapped.DataKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx, `INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id, wrapped_key, wrap_nonce) VALUES ($1,$2,$3,$4,$5,$6)`, refs[n], sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.WrappedKey, sealed.WrapNonce); err != nil {
			t.Fatal(err)
		}
	}
	const selectedRef = "credential_00"
	sandboxPool := pgtest.Login(t, owner, "upmo_sandbox")
	for n := 1; n <= 100; n++ {
		taskID := fmt.Sprintf("018f3b2c-7a10-7c4a-8e11-%012d", n)
		sandboxID := fmt.Sprintf("sb-rls-%03d", n)
		if _, err := sandboxPool.Exec(ctx, `SELECT gateway.attach_sandbox_credential($1::uuid,$2,$3)`, taskID, sandboxID, secretHash[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := sandboxPool.Exec(ctx, `SELECT gateway.bind_sandbox_credential($1,$2,$3)`, sandboxID, refs[(n-1)%len(refs)], secretHash[:]); err != nil {
			t.Fatal(err)
		}
	}
	for query, want := range map[string]int{"SELECT count(*) FROM gateway.credentials": 20, "SELECT count(*) FROM gateway.sandbox_credential_refs": 100} {
		if err := owner.QueryRow(ctx, query).Scan(&count); err != nil || count != want {
			t.Fatalf("fixture count for %q = %d, want %d err=%v", query, count, want, err)
		}
	}
	verify := keyservice.Verify{}
	tokens := map[string]string{}
	for _, audience := range []string{svcsig.AudienceWorkerBrain, svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		verify[audience] = [][]byte{raw}
		tokens[audience] = "upmo_svc_" + audience + "_" + base64.RawStdEncoding.EncodeToString(raw)
	}
	workerHandler, err := keyservice.New(owner, ring, verify, func(context.Context, string, []byte, func() error) (workerkeys.ProviderTestResponse, error) {
		return workerkeys.ProviderTestResponse{}, nil
	}, tracerPasswords{}, tracerEmails{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerServer := httptest.NewServer(workerHandler)
	t.Cleanup(workerServer.Close)
	keysClient, err := workerkeys.Open(workerkeys.Options{URL: workerServer.URL, Key: tokens[svcsig.AudienceWorkerSandboxd], Audience: svcsig.AudienceWorkerSandboxd, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	const sandboxID = "sb-rls-001"
	var current string
	if err := sandboxPool.QueryRow(ctx, `SELECT current_user`).Scan(&current); err != nil || current != "upmo_sandbox" {
		t.Fatalf("RLS role=%q err=%v", current, err)
	}
	sentinel := "upmo_sentinel_" + strings.Repeat("a", 64)
	credential := runtime.Credential{Sentinel: sentinel, Ref: selectedRef, Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}
	book := &tracerBook{principal: egress.Principal{ID: sandboxID, Allow: []string{"example.com"}, Creds: []runtime.Credential{credential}, Lease: 7}, source: netip.MustParseAddr("203.0.113.9")}
	copy(book.secret[:], secretHash[:])
	var callsMu sync.Mutex
	forCalls, dialCalls := 0, 0
	dialEntered := make(chan struct{}, egress.MaxConcurrentForwards)
	continueDial := make(chan struct{})
	park := false
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+live[0] {
			t.Errorf("upstream auth=%q", got)
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = io.WriteString(gz, strings.Repeat("x", 1<<20)+live[0])
		_ = gz.Close()
	}))
	t.Cleanup(upstream.Close)
	proxy := &egress.Proxy{Book: book, TLSRootCAs: upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, Inject: inject.Injector{Sandbox: inject.WorkerResolver{DB: sandboxPool, Keys: &keysClient}}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		callsMu.Lock()
		dialCalls++
		blocked := park
		if blocked {
			dialEntered <- struct{}{}
		}
		callsMu.Unlock()
		if blocked {
			select {
			case <-continueDial:
			case <-ctx.Done():
			}
		}
		var d net.Dialer
		return d.DialContext(ctx, network, upstream.Listener.Addr().String())
	}}
	proxy.Inject = countingSandboxInjector{inner: proxy.Inject, calls: &forCalls, mu: &callsMu}
	makeRequest := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Authorization", "Bearer "+sentinel)
		auth := &http.Request{Header: http.Header{}}
		auth.SetBasicAuth("ignored", base64.RawURLEncoding.EncodeToString(proxySecret))
		r.Header.Set("Proxy-Authorization", auth.Header.Get("Authorization"))
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		return w
	}
	var before, after goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	start := time.Now()
	results := make([]*httptest.ResponseRecorder, egress.MaxConcurrentForwards)
	latencies := make([]time.Duration, egress.MaxConcurrentForwards)
	done := make(chan struct{}, egress.MaxConcurrentForwards)
	for n := range results {
		go func(i int) {
			requestStart := time.Now()
			results[i] = makeRequest()
			latencies[i] = time.Since(requestStart)
			done <- struct{}{}
		}(n)
	}
	for range results {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent worker proxy request timed out")
		}
	}
	elapsed := time.Since(start)
	goruntime.ReadMemStats(&after)
	if elapsed >= 2*time.Second || after.HeapAlloc-before.HeapAlloc > 12<<20 || after.HeapAlloc-before.HeapAlloc > 256<<20 {
		t.Fatalf("concurrency budget elapsed=%s heap delta=%d", elapsed, after.HeapAlloc-before.HeapAlloc)
	}
	for i, result := range results {
		if latencies[i] >= 500*time.Millisecond {
			t.Errorf("request %d latency=%s", i, latencies[i])
		}
		if result.Code != http.StatusOK || strings.Contains(result.Body.String(), live[0]) || !strings.Contains(result.Body.String(), sentinel) {
			t.Fatalf("worker resolve status=%d leaked=%t", result.Code, strings.Contains(result.Body.String(), live[0]))
		}
	}
	callsMu.Lock()
	if forCalls != egress.MaxConcurrentForwards || dialCalls != egress.MaxConcurrentForwards {
		callsMu.Unlock()
		t.Fatalf("For=%d dial=%d", forCalls, dialCalls)
	}
	park = true
	callsMu.Unlock()
	parkedDone := make(chan *httptest.ResponseRecorder, egress.MaxConcurrentForwards)
	for range egress.MaxConcurrentForwards {
		go func() { parkedDone <- makeRequest() }()
	}
	for range egress.MaxConcurrentForwards {
		select {
		case <-dialEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("forward slot did not park in dial")
		}
	}
	callsMu.Lock()
	beforeFor, beforeDial := forCalls, dialCalls
	callsMu.Unlock()
	fifth := makeRequest()
	callsMu.Lock()
	afterFor, afterDial := forCalls, dialCalls
	callsMu.Unlock()
	if fifth.Code != http.StatusServiceUnavailable || !strings.Contains(fifth.Body.String(), "proxy_busy") || afterFor != beforeFor || afterDial != beforeDial {
		t.Fatalf("fifth status=%d For %d->%d dial %d->%d", fifth.Code, beforeFor, afterFor, beforeDial, afterDial)
	}
	close(continueDial)
	for range egress.MaxConcurrentForwards {
		select {
		case result := <-parkedDone:
			if result.Code != http.StatusOK || strings.Contains(result.Body.String(), live[0]) {
				t.Fatalf("parked forward status=%d leaked=%t", result.Code, strings.Contains(result.Body.String(), live[0]))
			}
		case <-time.After(3 * time.Second):
			t.Fatal("parked forward did not finish")
		}
	}
}

type countingSandboxInjector struct {
	inner egress.Injector
	calls *int
	mu    *sync.Mutex
}

func (c countingSandboxInjector) For(ctx context.Context, sandboxID string, secret []byte, host string, creds []runtime.Credential) (inject.Rewrite, error) {
	c.mu.Lock()
	*c.calls++
	c.mu.Unlock()
	return c.inner.For(ctx, sandboxID, secret, host, creds)
}

func pgCodeIs(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
