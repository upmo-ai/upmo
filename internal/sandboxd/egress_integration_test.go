//go:build integration

package sandboxd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/docker"
	"github.com/upmo-ai/upmo/internal/sandboxd/egress"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
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
	netName := testNetwork()
	if _, err := cli.NetworkInspect(ctx, netName, client.NetworkInspectOptions{}); cerrdefs.IsNotFound(err) {
		if _, err := cli.NetworkCreate(ctx, netName, client.NetworkCreateOptions{Driver: "bridge", Internal: true}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = cli.NetworkRemove(context.Background(), netName, client.NetworkRemoveOptions{}) })
	} else if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.NetworkConnect(ctx, netName, client.NetworkConnectOptions{Container: self}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = cli.NetworkDisconnect(context.Background(), netName, client.NetworkDisconnectOptions{Container: self, Force: true})
	})
	inspect, err := cli.ContainerInspect(ctx, self, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ep := inspect.Container.NetworkSettings.Networks[netName]
	if ep == nil || !ep.IPAddress.IsValid() {
		t.Fatalf("no address on %s: %+v", netName, ep)
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

	ln, err := net.Listen("tcp", net.JoinHostPort(ep.IPAddress.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := "http://" + ln.Addr().String()
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
	p := egress.Proxy{
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

	if got := sh(`echo "$HTTPS_PROXY"`); strings.TrimSpace(got) != "http://"+box.ID+":@"+ln.Addr().String() {
		t.Fatalf("HTTPS_PROXY = %q", got)
	}

	// CONNECT through the proxy named in HTTPS_PROXY, with bash's /dev/tcp,
	// since the base image has no curl.
	connect := func(target string) string {
		return sh(fmt.Sprintf(`set -e
hp=${HTTPS_PROXY#http://}; hp=${hp#*@}
exec 3<>/dev/tcp/${hp%%:*}/${hp##*:}
auth=$(printf '%%s:' %q | base64 -w0)
printf 'CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %%s\r\n\r\n' "$auth" >&3
IFS= read -r status <&3; echo "${status%%$'\r'}"
case "$status" in *" 200 "*) ;; *) exit 0 ;; esac
IFS= read -r _ <&3
printf 'ping\n' >&3
IFS= read -r reply <&3; echo "$reply"`, box.ID, target, target))
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
