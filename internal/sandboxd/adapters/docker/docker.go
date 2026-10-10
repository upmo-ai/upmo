// Package docker is the local Docker Engine driver for the container tier.
// It talks to the engine named by DOCKER_HOST, or the default
// socket, through the official Engine API client.
//
// Every sandbox is one container with all capabilities dropped but a small
// set, no-new-privileges, the engine's default seccomp and AppArmor profiles,
// docker-init as PID 1 and the fixed resource shape. Its private
// scratch volume is mounted at /scratch. No host path and no engine socket is
// ever mounted into a sandbox. User namespaces are a daemon setting
// (userns-remap or rootless Docker); the driver reports them but cannot turn
// them on per container.
//
// Every sandbox joins one internal bridge network (Options.Network), which
// has no route off the bridge and no outside DNS. The only thing a sandbox
// can reach there is the sandboxd egress proxy, which is attached to the
// same network; HTTP_PROXY, HTTPS_PROXY and ALL_PROXY point at it with the
// sandbox id as the proxy user name. The driver creates the network when it
// is missing and refuses one that is not internal.
//
// Commands run under the image's timeout program, so an image must ship
// coreutils or busybox timeout and a sleep that accepts infinity.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/upmo-ai/upmo/internal/runtime"
)

const (
	// ScratchDir is where the scratch volume is mounted, and where a
	// relative path or a command with no directory starts.
	ScratchDir = "/scratch"
	// DefaultMemory is the memory limit of a sandbox. The runtime sets none, so
	// the driver bounds it to keep one sandbox from starving the host.
	DefaultMemory int64 = 2 << 30
	// DefaultNetwork is the internal bridge every sandbox joins.
	DefaultNetwork = "upmo-sandbox-egress"
	// DefaultProxyURL is where a sandbox finds the egress proxy when nothing
	// else is set: sandboxd on the engine host. Compose sets the sandboxd
	// service on the sandbox network instead, because an internal network
	// cannot reach the engine host.
	DefaultProxyURL = "http://host.docker.internal:3128"
	// DefaultEditorImage is the code editor when Options.EditorImage and
	// UPMO_EDITOR_IMAGE are empty. Override to use an install-built image.
	DefaultEditorImage = "gitpod/openvscode-server:1.92.1"

	namePrefix = "upmo-sandbox-"

	labelSandbox = "ai.upmo.sandbox"
	labelTask    = "ai.upmo.task"
	labelTier    = "ai.upmo.tier"
	labelExpires = "ai.upmo.expires"
	labelNetwork = "ai.upmo.sandbox-network"

	pingTimeout  = 5 * time.Second
	stopSeconds  = 5
	cleanupLimit = 30 * time.Second
)

// keepCaps are the capabilities a sandbox keeps after dropping all: enough
// for a package manager and file ownership, without raw sockets, mknod or
// chroot.
var keepCaps = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETGID", "SETUID", "NET_BIND_SERVICE"}

// ErrPaused reports a command sent to a paused sandbox. Its files can still
// be read and written. It is the port's error, so the HTTP routes can name
// it without importing this driver.
var ErrPaused = runtime.ErrPaused

// engine is the part of the Engine API client the driver uses.
type engine interface {
	ImageInspect(ctx context.Context, image string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImagePull(ctx context.Context, ref string, opts client.ImagePullOptions) (client.ImagePullResponse, error)
	ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(ctx context.Context, id string, opts client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerList(ctx context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerSetLabels(ctx context.Context, name string, labels map[string]string) error
	VolumeRemove(ctx context.Context, id string, opts client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
	NetworkInspect(ctx context.Context, id string, opts client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	NetworkCreate(ctx context.Context, name string, opts client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	ExecCreate(ctx context.Context, id string, opts client.ExecCreateOptions) (client.ExecCreateResult, error)
	ExecAttach(ctx context.Context, id string, opts client.ExecAttachOptions) (client.ExecAttachResult, error)
	ExecInspect(ctx context.Context, id string, opts client.ExecInspectOptions) (client.ExecInspectResult, error)
	CopyToContainer(ctx context.Context, id string, opts client.CopyToContainerOptions) (client.CopyToContainerResult, error)
	CopyFromContainer(ctx context.Context, id string, opts client.CopyFromContainerOptions) (client.CopyFromContainerResult, error)
}

// Engine describes the daemon the driver reached.
type Engine struct {
	Host     string
	Version  string
	Rootless bool
	UserNS   bool
	Seccomp  bool
}

// Options tune the driver. The zero value is the default.
type Options struct {
	// Memory is the memory limit of each sandbox in bytes; 0 means DefaultMemory.
	Memory int64
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Network is the internal bridge network sandboxes join; empty means
	// DefaultNetwork.
	Network string
	// ProxyURL is the egress proxy as a sandbox reaches it, an http URL with
	// no user, path or query; empty means DefaultProxyURL.
	ProxyURL string
	// EditorImage is the code editor container image; empty means DefaultEditorImage.
	EditorImage string
}

// Driver runs sandboxes as Docker containers. It keeps the egress book in
// memory; the containers carry labels, so Sweep can still find one an
// earlier process left behind.
type Driver struct {
	api     engine
	closer  io.Closer
	memory  int64
	now     func() time.Time
	engine  Engine
	network string
	proxy        *url.URL
	editorImage  string

	mu    sync.Mutex
	boxes map[string]box
}

type box struct {
	spec    runtime.Spec
	expires time.Time
	paused  bool
}

var _ runtime.SandboxProvider = (*Driver)(nil)

// Open connects to the engine from the environment (DOCKER_HOST and the
// other DOCKER_ variables, or the default socket) and checks it answers.
func Open(ctx context.Context, opts Options) (*Driver, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker: client: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	ping, err := cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("docker: engine at %s: %w", cli.DaemonHost(), err)
	}
	if ping.OSType != "" && ping.OSType != "linux" {
		_ = cli.Close()
		return nil, fmt.Errorf("docker: engine at %s runs %s containers, not linux", cli.DaemonHost(), ping.OSType)
	}
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("docker: engine info: %w", err)
	}
	d, err := newDriver(cliEngine{cli}, opts)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}
	d.closer = cli
	d.engine = Engine{Host: cli.DaemonHost(), Version: info.Info.ServerVersion}
	for _, opt := range info.Info.SecurityOptions {
		switch {
		case strings.Contains(opt, "name=rootless"):
			d.engine.Rootless = true
		case strings.Contains(opt, "name=userns"):
			d.engine.UserNS = true
		case strings.Contains(opt, "name=seccomp"):
			d.engine.Seccomp = true
		}
	}
	return d, nil
}

func newDriver(api engine, opts Options) (*Driver, error) {
	if opts.Memory <= 0 {
		opts.Memory = DefaultMemory
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	netName := strings.TrimSpace(opts.Network)
	if netName == "" {
		netName = DefaultNetwork
	}
	raw := strings.TrimSpace(opts.ProxyURL)
	if raw == "" {
		raw = DefaultProxyURL
	}
	proxy, err := ParseProxyURL(raw)
	if err != nil {
		return nil, err
	}
	return &Driver{
		api:         api,
		memory:      opts.Memory,
		now:         opts.Now,
		network:     netName,
		proxy:       proxy,
		editorImage: strings.TrimSpace(opts.EditorImage),
		boxes:       map[string]box{},
	}, nil
}

func (d *Driver) editorImageRef() string {
	if img := strings.TrimSpace(d.editorImage); img != "" {
		return img
	}
	return DefaultEditorImage
}

// ParseProxyURL checks raw is an http URL with a host and no user, path,
// query or fragment, so the driver can add the sandbox id as the user.
func ParseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("docker: proxy URL %q must be http://host[:port]", raw)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// Engine returns what Open learnt about the daemon.
func (d *Driver) Engine() Engine { return d.engine }

// Network returns the name of the network sandboxes join.
func (d *Driver) Network() string { return d.network }

// Close releases the engine client.
func (d *Driver) Close() error {
	if d.closer == nil {
		return nil
	}
	return d.closer.Close()
}

// Create admits spec, makes sure the image and the sandbox network are
// present, and starts a container on that network, pointed at the egress
// proxy, with the fixed resource shape.
func (d *Driver) Create(ctx context.Context, spec runtime.Spec) (runtime.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return runtime.Sandbox{}, err
	}
	spec, err := runtime.CheckSpec(spec)
	if err != nil {
		return runtime.Sandbox{}, err
	}
	place, err := runtime.Place(spec.Tier, runtime.PlatformCompose)
	if err != nil {
		return runtime.Sandbox{}, runtime.ErrIsolation
	}
	uid, err := uuid.NewV7()
	if err != nil {
		return runtime.Sandbox{}, err
	}
	id := uid.String()
	if err := d.ensureImage(ctx, spec.Image); err != nil {
		return runtime.Sandbox{}, err
	}
	if err := d.ensureNetwork(ctx); err != nil {
		return runtime.Sandbox{}, err
	}

	expires := d.now().UTC().Add(spec.TTL)
	shape := runtime.ResourceShape()
	labels := map[string]string{
		labelSandbox: id,
		labelTask:    spec.Task,
		labelTier:    spec.Tier,
		labelExpires: expires.Format(time.RFC3339Nano),
	}
	pids := int64(shape.PIDs)
	init := true
	opts := client.ContainerCreateOptions{
		Name: containerName(id),
		Config: &container.Config{
			Image:      spec.Image,
			Entrypoint: []string{"sleep", "infinity"},
			Cmd:        []string{},
			WorkingDir: ScratchDir,
			Hostname:   "sandbox",
			Labels:     labels,
			Env:        d.proxyEnv(id),
		},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{d.network: {}},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(d.network),
			Runtime:       place.Runtime,
			CapDrop:       []string{"ALL"},
			CapAdd:        slices.Clone(keepCaps),
			SecurityOpt:   []string{"no-new-privileges:true"},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
			Init:          &init,
			Resources: container.Resources{
				NanoCPUs:   int64(shape.Burst) * 1e9,
				CPUShares:  int64(shape.CPU) * 1024,
				PidsLimit:  &pids,
				Memory:     d.memory,
				MemorySwap: d.memory,
			},
			Mounts: []mount.Mount{{
				Type:          mount.TypeVolume,
				Source:        volumeName(id),
				Target:        ScratchDir,
				VolumeOptions: &mount.VolumeOptions{NoCopy: true, Labels: labels},
			}},
		},
	}
	if _, err := d.api.ContainerCreate(ctx, opts); err != nil {
		d.cleanup(ctx, id)
		return runtime.Sandbox{}, fmt.Errorf("docker: create: %w", err)
	}
	if _, err := d.api.ContainerStart(ctx, containerName(id), client.ContainerStartOptions{}); err != nil {
		d.cleanup(ctx, id)
		return runtime.Sandbox{}, fmt.Errorf("docker: start: %w", err)
	}

	d.mu.Lock()
	d.boxes[id] = box{spec: spec, expires: expires}
	d.mu.Unlock()
	return runtime.Sandbox{ID: id, Tier: spec.Tier, Shape: shape, Expires: expires}, nil
}

// proxyEnv points every common proxy variable at the egress proxy, with the
// sandbox id as the Basic user name the proxy reads. Both cases are set,
// because curl reads only the lower-case http_proxy. NO_PROXY stays unset,
// so every HTTP and HTTPS request goes to the proxy.
func (d *Driver) proxyEnv(id string) []string {
	u := *d.proxy
	u.User = url.UserPassword(id, "")
	v := u.String()
	env := make([]string, 0, 6)
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		env = append(env, k+"="+v, strings.ToLower(k)+"="+v)
	}
	return env
}

// ensureNetwork makes sure the sandbox network exists and is internal. A
// network that is not internal has a route off the bridge, so a sandbox on
// it could bypass the proxy; the driver refuses it rather than use it.
func (d *Driver) ensureNetwork(ctx context.Context) error {
	for range 2 {
		res, err := d.api.NetworkInspect(ctx, d.network, client.NetworkInspectOptions{})
		if err == nil {
			if !res.Network.Internal {
				return fmt.Errorf("docker: network %s is not internal; a sandbox on it could bypass the egress proxy", d.network)
			}
			return nil
		}
		if !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("docker: network %s: %w", d.network, err)
		}
		_, err = d.api.NetworkCreate(ctx, d.network, client.NetworkCreateOptions{
			Driver:   "bridge",
			Internal: true,
			Labels:   map[string]string{labelNetwork: "egress"},
		})
		if err == nil {
			return nil
		}
		// Another Create made it first: inspect it again.
		if !cerrdefs.IsConflict(err) {
			return fmt.Errorf("docker: create network %s: %w", d.network, err)
		}
	}
	return fmt.Errorf("docker: network %s: still missing after create", d.network)
}

// ensureImage uses a local image when there is one and pulls it otherwise.
func (d *Driver) ensureImage(ctx context.Context, image string) error {
	_, err := d.api.ImageInspect(ctx, image)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("docker: image %s: %w", image, err)
	}
	pull, err := d.api.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull %s: %w", image, err)
	}
	defer func() { _ = pull.Close() }()
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("docker: pull %s: %w", image, err)
	}
	return nil
}

// Pause stops the container and keeps it and its scratch volume, so the
// files stay readable while the sandbox holds no CPU or memory.
func (d *Driver) Pause(ctx context.Context, id string) error {
	id, _, ok := d.live(id)
	if !ok {
		return runtime.ErrNotFound
	}
	timeout := stopSeconds
	if _, err := d.api.ContainerStop(ctx, containerName(id), client.ContainerStopOptions{Timeout: &timeout}); err != nil {
		if cerrdefs.IsNotFound(err) {
			d.forget(id)
			return runtime.ErrNotFound
		}
		return fmt.Errorf("docker: stop: %w", err)
	}
	d.mu.Lock()
	if b, ok := d.boxes[id]; ok {
		b.paused = true
		d.boxes[id] = b
	}
	d.mu.Unlock()
	return nil
}

// Destroy removes the container, its anonymous volumes and its scratch volume.
func (d *Driver) Destroy(ctx context.Context, id string) error {
	id, _, ok := d.live(id)
	if !ok {
		return runtime.ErrNotFound
	}
	if err := d.remove(ctx, id); err != nil {
		return err
	}
	d.forget(id)
	return nil
}

// Sweep removes expired sandbox containers and idle code editors, and returns
// how many it removed.
func (d *Driver) Sweep(ctx context.Context) (int, error) {
	n, err := d.sweepExpiredSandboxes(ctx)
	if err != nil {
		return n, err
	}
	idle, err := d.sweepIdleEditors(ctx)
	return n + idle, err
}

func (d *Driver) sweepExpiredSandboxes(ctx context.Context) (int, error) {
	list, err := d.api.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", labelSandbox),
	})
	if err != nil {
		return 0, fmt.Errorf("docker: list: %w", err)
	}
	now := d.now().UTC()
	removed := 0
	var errs []error
	for _, c := range list.Items {
		if c.Labels[labelEditor] == "1" {
			continue
		}
		id := c.Labels[labelSandbox]
		if _, err := uuid.Parse(id); err != nil {
			continue
		}
		// An expiry the driver cannot read is treated as passed: the
		// container carries the sandbox label, so it is ours to reclaim.
		if expires, err := time.Parse(time.RFC3339Nano, c.Labels[labelExpires]); err == nil && now.Before(expires) {
			continue
		}
		if err := d.remove(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		}
		d.forget(id)
		removed++
	}
	return removed, errors.Join(errs...)
}

// Egress returns the allow-list and credentials of a live, unpaused sandbox
// for the egress proxy.
func (d *Driver) Egress(id string) ([]string, []runtime.Credential, bool) {
	_, b, ok := d.live(id)
	if !ok || b.paused {
		return nil, nil, false
	}
	return slices.Clone(b.spec.Allow), slices.Clone(b.spec.Credentials), true
}

// PTY is not built in the Docker driver.
func (d *Driver) PTY(context.Context, string, runtime.Size) (io.ReadWriteCloser, error) {
	return nil, runtime.ErrUnsupported
}

// Expose is not built in the Docker driver.
func (d *Driver) Expose(context.Context, string, int) (runtime.Endpoint, error) {
	return runtime.Endpoint{}, runtime.ErrUnsupported
}

// live returns the trimmed id and the sandbox, and forgets one whose TTL has
// passed; Sweep reclaims its container.
func (d *Driver) live(id string) (string, box, bool) {
	id = strings.TrimSpace(id)
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.boxes[id]
	if !ok {
		return id, box{}, false
	}
	if !d.now().UTC().Before(b.expires) {
		delete(d.boxes, id)
		return id, box{}, false
	}
	return id, b, true
}

func (d *Driver) forget(id string) {
	d.mu.Lock()
	delete(d.boxes, id)
	d.mu.Unlock()
}

// remove force-removes the container with its anonymous volumes, then the
// named scratch volume. Either already gone is not an error.
func (d *Driver) remove(ctx context.Context, id string) error {
	if err := d.removeEditor(ctx, id); err != nil {
		return err
	}
	_, err := d.api.ContainerRemove(ctx, containerName(id), client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("docker: remove: %w", err)
	}
	_, err = d.api.VolumeRemove(ctx, volumeName(id), client.VolumeRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("docker: remove volume: %w", err)
	}
	return nil
}

// cleanup removes what a failed Create left behind, even when ctx has ended.
func (d *Driver) cleanup(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupLimit)
	defer cancel()
	_ = d.remove(ctx, id)
}

func containerName(id string) string { return namePrefix + id }

func volumeName(id string) string { return namePrefix + id + "-scratch" }
