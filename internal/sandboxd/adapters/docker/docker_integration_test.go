//go:build integration

package docker

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// openEngine connects to the engine at UPMO_TEST_DOCKER_HOST. The name is
// separate from DOCKER_HOST, so the suite never starts containers on an
// engine nobody pointed it at. Unset skips; set but unreachable fails.
func openEngine(t *testing.T) (*Driver, *client.Client) {
	t.Helper()
	host := os.Getenv("UPMO_TEST_DOCKER_HOST")
	if host == "" {
		t.Skip("UPMO_TEST_DOCKER_HOST is not set; make test-integration sets it")
	}
	t.Setenv("DOCKER_HOST", host)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := Open(ctx, Options{Network: testNetwork()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return d, cli
}

// testNetwork keeps the suite off the network a running stack uses. Set
// UPMO_TEST_SANDBOX_NETWORK to an internal network made in advance (with an
// explicit subnet) on an engine whose address pools are exhausted.
func testNetwork() string {
	if v := os.Getenv("UPMO_TEST_SANDBOX_NETWORK"); v != "" {
		return v
	}
	return DefaultNetwork + "-test"
}

func image() string {
	if v := os.Getenv("UPMO_TEST_SANDBOX_IMAGE"); v != "" {
		return v
	}
	return "debian:bookworm-slim"
}

// run executes a shell line and returns stdout, stderr and the exit code.
func run(t *testing.T, d *Driver, id, line string) (string, string, int) {
	t.Helper()
	var stdout, stderr strings.Builder
	exit, err := d.Exec(context.Background(), id, runtime.Command{Args: []string{"sh", "-c", line}, Timeout: time.Minute},
		func(o runtime.Output) error {
			if o.Stream == "stderr" {
				stderr.Write(o.Data)
			} else {
				stdout.Write(o.Data)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return stdout.String(), stderr.String(), exit.Code
}

func TestDockerSandboxLifecycle(t *testing.T) {
	d, cli := openEngine(t)
	ctx := context.Background()

	box, err := d.Create(ctx, runtime.Spec{
		Task:  "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image: image(),
		Tier:  runtime.TierContainer,
		TTL:   10 * time.Minute,
		Allow: []string{"github.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	destroyed := false
	t.Cleanup(func() {
		if !destroyed {
			_ = d.Destroy(context.Background(), box.ID)
		}
	})

	out, errOut, code := run(t, d, box.ID, "echo out; echo err >&2; exit 3")
	if out != "out\n" || errOut != "err\n" || code != 3 {
		t.Fatalf("exec = %q %q %d", out, errOut, code)
	}
	if out, _, _ := run(t, d, box.ID, "pwd"); out != ScratchDir+"\n" {
		t.Fatalf("pwd = %q", out)
	}

	// One interface on the internal sandbox network, no default route and no
	// outside DNS: the only route out is the egress proxy.
	inspect, err := cli.ContainerInspect(ctx, containerName(box.ID), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if nets := inspect.Container.NetworkSettings.Networks; len(nets) != 1 || nets[sandboxNetworkName(box.ID)] == nil {
		t.Fatalf("networks = %v", nets)
	}
	if out, _, _ := run(t, d, box.ID, "ls /sys/class/net | sort | tr '\\n' ' '"); strings.TrimSpace(out) != "eth0 lo" {
		t.Fatalf("interfaces = %q", out)
	}
	if out, _, _ := run(t, d, box.ID, "awk 'NR>1 && $2==\"00000000\"' /proc/net/route"); strings.TrimSpace(out) != "" {
		t.Fatalf("default route = %q", out)
	}
	if _, _, code := run(t, d, box.ID, "getent hosts github.com"); code == 0 {
		t.Fatal("a sandbox resolved an outside name")
	}
	self, _ := os.Hostname()
	proxyInspect, err := cli.ContainerInspect(ctx, self, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	proxyIP := proxyInspect.Container.NetworkSettings.Networks[sandboxNetworkName(box.ID)].IPAddress.String()
	out, _, _ = run(t, d, box.ID, "echo $HTTPS_PROXY $https_proxy")
	proxyURLs := strings.Fields(out)
	if len(proxyURLs) != 2 || proxyURLs[0] != proxyURLs[1] {
		t.Fatal("proxy environment variables do not agree")
	}
	proxyURL, err := url.Parse(proxyURLs[0])
	if err != nil {
		t.Fatal("proxy URL is invalid")
	}
	password, _ := proxyURL.User.Password()
	secret, err := base64.RawURLEncoding.DecodeString(password)
	if err != nil || len(secret) != 32 || proxyURL.User.Username() != "sandbox" || proxyURL.Host != net.JoinHostPort(proxyIP, "3128") {
		clear(secret)
		t.Fatal("proxy URL has invalid credentials or endpoint")
	}
	clear(secret)
	if _, _, code := run(t, d, box.ID, "test -e /var/run/docker.sock || test -e /run/docker.sock"); code == 0 {
		t.Fatal("an engine socket is visible in the sandbox")
	}
	if out, _, _ := run(t, d, box.ID, "cat /sys/fs/cgroup/pids.max"); strings.TrimSpace(out) != "256" {
		t.Fatalf("pids.max = %q", out)
	}
	if out, _, _ := run(t, d, box.ID, "cat /sys/fs/cgroup/cpu.max"); !strings.HasPrefix(out, "200000 100000") {
		t.Fatalf("cpu.max = %q", out)
	}
	if out, _, _ := run(t, d, box.ID, "grep NoNewPrivs /proc/self/status"); !strings.Contains(out, "1") {
		t.Fatalf("no-new-privileges = %q", out)
	}

	if err := d.WriteFile(ctx, box.ID, "hello.txt", []byte("hi from sandboxd\n")); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := run(t, d, box.ID, "cat /scratch/hello.txt"); out != "hi from sandboxd\n" {
		t.Fatalf("cat = %q", out)
	}
	run(t, d, box.ID, "printf made-inside > /scratch/inside.txt")
	if got, err := d.ReadFile(ctx, box.ID, "/scratch/inside.txt"); err != nil || string(got) != "made-inside" {
		t.Fatalf("read = %q %v", got, err)
	}

	start := time.Now()
	_, err = d.Exec(ctx, box.ID, runtime.Command{Args: []string{"sleep", "30"}, Timeout: time.Second}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("timeout returned after %v", took)
	}
	// The engine-side timeout kills the command too.
	time.Sleep(2 * time.Second)
	if out, _, _ := run(t, d, box.ID, `for p in /proc/[0-9]*; do tr '\0' ' ' < $p/cmdline; echo; done | grep -c '^sleep 30 ' || true`); strings.TrimSpace(out) != "0" {
		t.Fatalf("sleep 30 still running: %q", out)
	}

	if err := d.Pause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	inspect, err = cli.ContainerInspect(ctx, containerName(box.ID), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Container.State.Running {
		t.Fatal("a paused sandbox is still running")
	}
	if _, err := d.Exec(ctx, box.ID, runtime.Command{Args: []string{"true"}}, nil); !errors.Is(err, ErrPaused) {
		t.Fatalf("exec on paused err = %v", err)
	}
	if got, err := d.ReadFile(ctx, box.ID, "hello.txt"); err != nil || string(got) != "hi from sandboxd\n" {
		t.Fatalf("read on paused = %q %v", got, err)
	}

	if err := d.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	destroyed = true
	if _, err := cli.ContainerInspect(ctx, containerName(box.ID), client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("container after destroy err = %v", err)
	}
	if _, err := cli.VolumeInspect(ctx, volumeName(box.ID), client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("scratch after destroy err = %v", err)
	}
	if err := d.Destroy(ctx, box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("second destroy err = %v", err)
	}
}

func TestDockerRefusesKata(t *testing.T) {
	d, _ := openEngine(t)
	_, err := d.Create(context.Background(), runtime.Spec{
		Task:  "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image: image(),
		Tier:  runtime.TierKata,
		TTL:   time.Minute,
	})
	if !errors.Is(err, runtime.ErrIsolation) {
		t.Fatalf("kata err = %v, want ErrIsolation", err)
	}
}

func TestDockerGVisorUsesRunscOrFailsClosed(t *testing.T) {
	d, cli := openEngine(t)
	ctx := context.Background()
	box, err := d.Create(ctx, runtime.Spec{
		Task:  "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image: image(),
		Tier:  runtime.TierGVisor,
		TTL:   time.Minute,
	})
	if err != nil {
		// No runsc on the engine: fail closed rather than start on the default.
		return
	}
	t.Cleanup(func() { _ = d.Destroy(context.WithoutCancel(ctx), box.ID) })
	inspect, err := cli.ContainerInspect(ctx, containerName(box.ID), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := inspect.Container.HostConfig.Runtime; got != runtime.RuntimeRunsc {
		t.Fatalf("runtime = %q, want %q", got, runtime.RuntimeRunsc)
	}
}

func TestDockerSweepReclaimsExpired(t *testing.T) {
	d, cli := openEngine(t)
	ctx := context.Background()
	box, err := d.Create(ctx, runtime.Spec{
		Task:  "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image: image(),
		Tier:  runtime.TierContainer,
		TTL:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.remove(context.Background(), box.ID) })
	time.Sleep(1100 * time.Millisecond)
	if _, err := d.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ContainerInspect(ctx, containerName(box.ID), client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("container after sweep err = %v", err)
	}
}
