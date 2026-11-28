package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"net"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// fake is an in-memory engine. It records what the driver asked for and
// answers each exec with the frames in script.
type fake struct {
	mu sync.Mutex

	images  map[string]bool
	pulled  []string
	created []client.ContainerCreateOptions
	started []string
	stopped []string
	removed []string
	volumes []string
	execs   []client.ExecCreateOptions
	copied  []client.CopyToContainerOptions
	files   map[string][]byte
	listed  []container.Summary

	// networks maps a network name to whether it is internal.
	networks   map[string]bool
	netCreated []client.NetworkCreateOptions
	netErr     error

	startErr error
	// script is the multiplexed output and exit code of the next exec. A nil
	// script holds the stream open until the driver closes it.
	script []frame
	exit   int
	conns  []net.Conn

	containers   map[string]fakeCont
	nextHostPort int
	nextEditorIP int
}

type fakeCont struct {
	name        string
	labels      map[string]string
	started     bool
	removed     bool
	hostPort    int
	networkIP   string
	networkName string
	opts        client.ContainerCreateOptions
}

type frame struct {
	stream byte
	data   string
}

func newFake() *fake {
	f := &fake{
		images:       map[string]bool{"upmo/sandbox:base": true, "upmo/editor:dev": true},
		files:        map[string][]byte{},
		networks:     map[string]bool{},
		containers:   map[string]fakeCont{},
		nextHostPort: 30000,
	}
	self, _ := os.Hostname()
	f.containers[self] = fakeCont{name: self}
	return f
}

func (f *fake) NetworkInspect(_ context.Context, id string, _ client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.netErr != nil {
		return client.NetworkInspectResult{}, f.netErr
	}
	internal, ok := f.networks[id]
	if !ok {
		return client.NetworkInspectResult{}, cerrdefs.ErrNotFound
	}
	var res client.NetworkInspectResult
	res.Network.Name, res.Network.Internal = id, internal
	return res, nil
}

func (f *fake) NetworkCreate(_ context.Context, name string, opts client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netCreated = append(f.netCreated, opts)
	if _, ok := f.networks[name]; ok {
		return client.NetworkCreateResult{}, cerrdefs.ErrConflict
	}
	f.networks[name] = opts.Internal
	return client.NetworkCreateResult{ID: "n-" + name}, nil
}

func (f *fake) NetworkConnect(_ context.Context, name string, opts client.NetworkConnectOptions) (client.NetworkConnectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[opts.Container]
	if !ok {
		return client.NetworkConnectResult{}, cerrdefs.ErrNotFound
	}
	c.networkName = name
	c.networkIP = "172.20.0.2"
	f.containers[opts.Container] = c
	return client.NetworkConnectResult{}, nil
}
func (f *fake) NetworkDisconnect(_ context.Context, _ string, _ client.NetworkDisconnectOptions) (client.NetworkDisconnectResult, error) {
	return client.NetworkDisconnectResult{}, nil
}
func (f *fake) NetworkRemove(_ context.Context, name string, _ client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	f.mu.Lock()
	delete(f.networks, name)
	f.mu.Unlock()
	return client.NetworkRemoveResult{}, nil
}

func (f *fake) ImageInspect(_ context.Context, image string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.images[image] {
		return client.ImageInspectResult{}, cerrdefs.ErrNotFound
	}
	return client.ImageInspectResult{}, nil
}

func (f *fake) ImagePull(_ context.Context, ref string, _ client.ImagePullOptions) (client.ImagePullResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	f.images[ref] = true
	return pullStream{io.NopCloser(strings.NewReader(""))}, nil
}

type pullStream struct{ io.ReadCloser }

func (pullStream) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}
func (pullStream) Wait(context.Context) error { return nil }

func (f *fake) ContainerCreate(_ context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, opts)
	hostPort := 0
	if opts.HostConfig != nil && len(opts.HostConfig.PortBindings) > 0 {
		f.nextHostPort++
		hostPort = f.nextHostPort
	}
	networkIP, networkName := "", ""
	if opts.NetworkingConfig != nil {
		for n := range opts.NetworkingConfig.EndpointsConfig {
			networkName = n
			break
		}
		if networkName != "" {
			f.nextEditorIP++
			networkIP = fmt.Sprintf("172.20.0.%d", f.nextEditorIP+2)
		}
	}
	if opts.Config != nil && opts.Config.Labels[labelEditor] == "1" {
		if networkIP == "" {
			f.nextEditorIP++
			networkIP = fmt.Sprintf("172.20.0.%d", f.nextEditorIP)
		}
	}
	labels := map[string]string{}
	if opts.Config != nil {
		for k, v := range opts.Config.Labels {
			labels[k] = v
		}
	}
	f.containers[opts.Name] = fakeCont{name: opts.Name, labels: labels, hostPort: hostPort, networkIP: networkIP, networkName: networkName, opts: opts}
	return client.ContainerCreateResult{ID: "c-" + opts.Name}, nil
}

func (f *fake) ContainerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
	if c, ok := f.containers[id]; ok {
		c.started = true
		f.containers[id] = c
	}
	return client.ContainerStartResult{}, f.startErr
}

func (f *fake) ContainerStop(_ context.Context, id string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return client.ContainerStopResult{}, nil
}

func (f *fake) ContainerRemove(_ context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !opts.Force {
		return client.ContainerRemoveResult{}, errors.New("remove without force")
	}
	c, ok := f.containers[id]
	if !ok || c.removed {
		if opts.RemoveVolumes {
			// Sweep lists containers an earlier process left behind; they may
			// not be in this fake's create log.
			f.removed = append(f.removed, id)
			return client.ContainerRemoveResult{}, nil
		}
		return client.ContainerRemoveResult{}, cerrdefs.ErrNotFound
	}
	f.removed = append(f.removed, id)
	c.removed = true
	f.containers[id] = c
	return client.ContainerRemoveResult{}, nil
}

func (f *fake) ContainerList(_ context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	labelFilters := opts.Filters["label"]
	if len(labelFilters) == 0 {
		return client.ContainerListResult{}, errors.New("list without label filters")
	}
	// Sweep uses only the sandbox label key; return scripted items.
	if len(labelFilters) == 1 && labelFilters[labelSandbox] {
		if !opts.All {
			return client.ContainerListResult{}, errors.New("list sweep without all")
		}
		return client.ContainerListResult{Items: slices.Clone(f.listed)}, nil
	}
	var items []container.Summary
	for _, c := range f.containers {
		if c.removed || (!opts.All && !c.started) {
			continue
		}
		if !matchLabelFilterMap(c.labels, labelFilters) {
			continue
		}
		state := container.StateCreated
		if c.started {
			state = container.StateRunning
		}
		items = append(items, container.Summary{Labels: mapsClone(c.labels), State: state})
	}
	return client.ContainerListResult{Items: items}, nil
}

func (f *fake) ContainerSetLabels(_ context.Context, name string, labels map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[name]
	if !ok || c.removed {
		return cerrdefs.ErrNotFound
	}
	for k, v := range labels {
		c.labels[k] = v
	}
	if c.opts.Config != nil {
		if c.opts.Config.Labels == nil {
			c.opts.Config.Labels = map[string]string{}
		}
		for k, v := range labels {
			c.opts.Config.Labels[k] = v
		}
	}
	f.containers[name] = c
	return nil
}

func (f *fake) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok || c.removed {
		return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
	}
	var res client.ContainerInspectResult
	res.Container.Config = &container.Config{Labels: mapsClone(c.labels)}
	if c.networkIP != "" {
		res.Container.NetworkSettings = &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				c.networkName: {IPAddress: netip.MustParseAddr(c.networkIP)},
			},
		}
	} else if c.hostPort > 0 {
		port, _ := network.ParsePort(fmt.Sprintf("%d/tcp", editorListenPort))
		res.Container.NetworkSettings = &container.NetworkSettings{
			Ports: network.PortMap{
				port: []network.PortBinding{{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: strconv.Itoa(c.hostPort)}},
			},
		}
	}
	return res, nil
}

func matchLabelFilterMap(labels map[string]string, filters map[string]bool) bool {
	for f, want := range filters {
		if !want {
			continue
		}
		key, val, ok := strings.Cut(f, "=")
		if ok {
			if labels[key] != val {
				return false
			}
			continue
		}
		if _, exists := labels[f]; !exists {
			return false
		}
	}
	return true
}

func mapsClone(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (f *fake) VolumeRemove(_ context.Context, id string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes = append(f.volumes, id)
	return client.VolumeRemoveResult{}, nil
}

func (f *fake) ExecCreate(_ context.Context, _ string, opts client.ExecCreateOptions) (client.ExecCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, opts)
	return client.ExecCreateResult{ID: "exec-1"}, nil
}

func (f *fake) ExecAttach(_ context.Context, _ string, _ client.ExecAttachOptions) (client.ExecAttachResult, error) {
	f.mu.Lock()
	script := f.script
	ours, theirs := net.Pipe()
	f.conns = append(f.conns, theirs)
	f.mu.Unlock()
	go func() {
		if script == nil {
			_, _ = io.Copy(io.Discard, theirs) // returns when the driver closes its end
			return
		}
		for _, fr := range script {
			hdr := make([]byte, 8)
			hdr[0] = fr.stream
			binary.BigEndian.PutUint32(hdr[4:], uint32(len(fr.data))) //nolint:gosec // test frames are a few bytes
			if _, err := theirs.Write(append(hdr, fr.data...)); err != nil {
				return
			}
		}
		_ = theirs.Close()
	}()
	return client.ExecAttachResult{HijackedResponse: client.HijackedResponse{Conn: ours, Reader: bufio.NewReader(ours)}}, nil
}

func (f *fake) ExecInspect(context.Context, string, client.ExecInspectOptions) (client.ExecInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return client.ExecInspectResult{ExitCode: f.exit}, nil
}

func (f *fake) CopyToContainer(_ context.Context, _ string, opts client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	raw, err := io.ReadAll(opts.Content)
	if err != nil {
		return client.CopyToContainerResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := copyDir(opts)
	if dir == "/missing/" {
		return client.CopyToContainerResult{}, cerrdefs.ErrNotFound
	}
	opts.Content = bytes.NewReader(raw)
	f.copied = append(f.copied, opts)
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return client.CopyToContainerResult{}, err
		}
		data, _ := io.ReadAll(tr)
		f.files[dir+hdr.Name] = data
	}
	return client.CopyToContainerResult{}, nil
}

// copyDir reads the dir field by position, for the reason copyOptions gives.
func copyDir(opts client.CopyToContainerOptions) string {
	return reflect.ValueOf(opts).Field(0).String()
}

func (f *fake) CopyFromContainer(_ context.Context, _ string, opts client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if opts.SourcePath == ScratchDir {
		return client.CopyFromContainerResult{
			Content: io.NopCloser(strings.NewReader("")),
			Stat:    container.PathStat{Name: "scratch", Mode: fs.ModeDir | 0o755},
		}, nil
	}
	data, ok := f.files[opts.SourcePath]
	if !ok {
		return client.CopyFromContainerResult{}, cerrdefs.ErrNotFound
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "f", Mode: 0o644, Size: int64(len(data))})
	_, _ = tw.Write(data)
	_ = tw.Close()
	return client.CopyFromContainerResult{
		Content: io.NopCloser(&buf),
		Stat:    container.PathStat{Name: "f", Size: int64(len(data)), Mode: 0o644},
	}, nil
}

func spec() runtime.Spec {
	return runtime.Spec{
		Task:        "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image:       "upmo/sandbox:base",
		Tier:        runtime.TierContainer,
		TTL:         time.Hour,
		Allow:       []string{"github.com"},
		Credentials: []runtime.Credential{{Sentinel: "upmo_sentinel_" + strings.Repeat("a", 64), Ref: "github_token", Host: "github.com", Methods: []string{"GET"}, Paths: []string{"/user"}}},
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*Driver, *fake, *clock) {
	t.Helper()
	return setupOpts(t, Options{})
}

func setupEditor(t *testing.T) (*Driver, *fake, *clock) {
	t.Helper()
	return setupOpts(t, Options{EditorImage: "upmo/editor:dev", EditorBaseURL: "http://127.0.0.1:8084"})
}

func setupOpts(t *testing.T, opts Options) (*Driver, *fake, *clock) {
	t.Helper()
	f := newFake()
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	opts.Now = c.now
	d, err := newDriver(f, opts)
	if err != nil {
		t.Fatal(err)
	}
	return d, f, c
}

func create(t *testing.T, d *Driver) runtime.Sandbox {
	t.Helper()
	box, err := d.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestCreateStartsAnIsolatedContainer(t *testing.T) {
	d, f, c := setup(t)
	box := create(t, d)
	if box.ID == "" || box.Tier != runtime.TierContainer || box.Shape != runtime.ResourceShape() || !box.Expires.Equal(c.now().Add(time.Hour)) {
		t.Fatalf("box = %+v", box)
	}
	if len(f.pulled) != 0 {
		t.Fatalf("pulled a local image: %v", f.pulled)
	}
	if len(f.created) != 1 || len(f.started) != 1 || f.started[0] != containerName(box.ID) {
		t.Fatalf("created %d, started %v", len(f.created), f.started)
	}
	opts := f.created[0]
	cfg, host := opts.Config, opts.HostConfig
	if opts.Name != containerName(box.ID) || cfg.Image != "upmo/sandbox:base" || cfg.WorkingDir != ScratchDir {
		t.Fatalf("create = %+v", opts)
	}
	if !slices.Equal(cfg.Entrypoint, []string{"sleep", "infinity"}) || host.Init == nil || !*host.Init {
		t.Fatalf("entrypoint %v init %v", cfg.Entrypoint, host.Init)
	}
	if host.Runtime != "" {
		t.Fatalf("container runtime = %q, want empty", host.Runtime)
	}

	// Only the internal sandbox network: the only route out is the sandboxd
	// proxy, and nothing is published.
	if string(host.NetworkMode) != sandboxNetworkName(box.ID) || cfg.NetworkDisabled || len(cfg.ExposedPorts) != 0 || len(host.PortBindings) != 0 || host.PublishAllPorts {
		t.Fatalf("network = %q disabled=%v", host.NetworkMode, cfg.NetworkDisabled)
	}
	if opts.NetworkingConfig == nil || len(opts.NetworkingConfig.EndpointsConfig) != 1 || opts.NetworkingConfig.EndpointsConfig[sandboxNetworkName(box.ID)] == nil {
		t.Fatalf("endpoints = %+v", opts.NetworkingConfig)
	}
	if len(host.ExtraHosts) != 0 || len(host.DNS) != 0 {
		t.Fatalf("extra hosts %v dns %v", host.ExtraHosts, host.DNS)
	}
	if internal, ok := f.networks[sandboxNetworkName(box.ID)]; !ok || !internal || len(f.netCreated) != 1 || f.netCreated[0].Driver != "bridge" {
		t.Fatalf("network created = %+v", f.netCreated)
	}
	checkProxyEnv(t, cfg.Env, "172.20.0.2")
	for _, e := range cfg.Env {
		if strings.HasPrefix(strings.ToUpper(e), "NO_PROXY=") || strings.Contains(e, "GITHUB_TOKEN") {
			t.Fatalf("env %q", e)
		}
	}
	// No host path, and so no engine socket, is mounted.
	if len(host.Binds) != 0 || len(host.VolumesFrom) != 0 || host.Privileged {
		t.Fatalf("binds %v volumes-from %v privileged %v", host.Binds, host.VolumesFrom, host.Privileged)
	}
	if len(host.Mounts) != 1 {
		t.Fatalf("mounts = %+v", host.Mounts)
	}
	m := host.Mounts[0]
	if m.Type != mount.TypeVolume || m.Source != volumeName(box.ID) || m.Target != ScratchDir || m.VolumeOptions == nil || !m.VolumeOptions.NoCopy {
		t.Fatalf("scratch mount = %+v", m)
	}
	if strings.Contains(m.Source, "docker.sock") || strings.HasPrefix(m.Source, "/") {
		t.Fatalf("scratch is a host path: %q", m.Source)
	}

	if !slices.Equal(host.CapDrop, []string{"ALL"}) || slices.Contains(host.CapAdd, "SYS_ADMIN") || slices.Contains(host.CapAdd, "NET_RAW") {
		t.Fatalf("caps drop %v add %v", host.CapDrop, host.CapAdd)
	}
	if !slices.Contains(host.SecurityOpt, "no-new-privileges:true") {
		t.Fatalf("security opts = %v", host.SecurityOpt)
	}
	for _, opt := range host.SecurityOpt {
		if strings.Contains(opt, "unconfined") {
			t.Fatalf("security opt %q turns a profile off", opt)
		}
	}
	if host.UsernsMode != "" || host.PidMode != "" || host.IpcMode != "" {
		t.Fatalf("shares a host namespace: userns %q pid %q ipc %q", host.UsernsMode, host.PidMode, host.IpcMode)
	}

	shape := runtime.ResourceShape()
	r := host.Resources
	if r.NanoCPUs != int64(shape.Burst)*1e9 || r.CPUShares != int64(shape.CPU)*1024 || r.PidsLimit == nil || *r.PidsLimit != int64(shape.PIDs) {
		t.Fatalf("resources = %+v", r)
	}
	if r.Memory != DefaultMemory || r.MemorySwap != DefaultMemory {
		t.Fatalf("memory = %d swap %d", r.Memory, r.MemorySwap)
	}

	if cfg.Labels[labelSandbox] != box.ID || cfg.Labels[labelTask] != spec().Task || cfg.Labels[labelTier] != runtime.TierContainer {
		t.Fatalf("labels = %v", cfg.Labels)
	}
	if exp, err := time.Parse(time.RFC3339Nano, cfg.Labels[labelExpires]); err != nil || !exp.Equal(box.Expires) {
		t.Fatalf("expires label = %q", cfg.Labels[labelExpires])
	}
	for k, v := range cfg.Labels {
		if strings.Contains(v, "GITHUB_TOKEN") || strings.Contains(v, "sentinel") {
			t.Fatalf("label %s carries a credential reference", k)
		}
	}

	allow, creds, ok := d.Egress(box.ID)
	if !ok || !slices.Equal(allow, []string{"github.com"}) || len(creds) != 1 {
		t.Fatalf("egress = %v %v %v", allow, creds, ok)
	}
}

func TestCreateUsesPerSandboxNetworkAndProxy(t *testing.T) {
	f := newFake()
	f.networks["upm114_sandbox-egress"] = true
	d, err := newDriver(f, Options{Network: " upm114_sandbox-egress ", ProxyURL: "http://sandboxd:3128/"})
	if err != nil {
		t.Fatal(err)
	}
	box := create(t, d)
	opts := f.created[0]
	if string(opts.HostConfig.NetworkMode) != sandboxNetworkName(box.ID) || opts.NetworkingConfig.EndpointsConfig[sandboxNetworkName(box.ID)] == nil || opts.NetworkingConfig.EndpointsConfig["upm114_sandbox-egress"] != nil {
		t.Fatalf("network = %q %+v", opts.HostConfig.NetworkMode, opts.NetworkingConfig)
	}
	checkProxyEnv(t, opts.Config.Env, "172.20.0.2")
	if d.Network() != "upm114_sandbox-egress" {
		t.Fatalf("Network() = %q", d.Network())
	}
}

func TestCreateRefusesANetworkWithARouteOut(t *testing.T) {
	d, f, _ := setup(t)
	f.networks["upmo-sbx-test"] = false
	if _, err := d.ensureSandboxNetwork(context.Background(), "upmo-sbx-test"); err == nil || !strings.Contains(err.Error(), "not internal") {
		t.Fatalf("err = %v", err)
	}
	f.networks = map[string]bool{}
	f.netErr = errors.New("engine down")
	if _, err := d.ensureSandboxNetwork(context.Background(), "upmo-sbx-test"); err == nil {
		t.Fatal("want an error when the network cannot be inspected")
	}
	if len(f.created) != 0 {
		t.Fatalf("a container was created without a safe network: %d", len(f.created))
	}
}

func TestDockerAuthenticateRevokesPausedExpiredAndStopped(t *testing.T) {
	d, _, _ := setup(t)
	box := create(t, d)
	secret := []byte(strings.Repeat("s", 32))
	ip := netip.MustParseAddr("172.20.0.10")
	d.mu.Lock()
	b := d.boxes[box.ID]
	b.ip, b.secret = ip, sha256.Sum256(secret)
	d.boxes[box.ID] = b
	d.mu.Unlock()
	p, ok := d.Authenticate(ip, secret)
	if !ok || p.ID != box.ID || p.Lease == 0 {
		t.Fatalf("Authenticate = %+v %t", p, ok)
	}
	if release, ctx, ok := d.Acquire(box.ID, p.Lease); !ok || ctx == nil {
		t.Fatal("Acquire refused live sandbox")
	} else {
		release()
	}
	if err := d.Pause(context.Background(), box.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Authenticate(ip, secret); ok {
		t.Fatal("authenticated paused sandbox")
	}

	d2, _, c2 := setup(t)
	expired := create(t, d2)
	d2.mu.Lock()
	b = d2.boxes[expired.ID]
	b.ip, b.secret = ip, sha256.Sum256(secret)
	d2.boxes[expired.ID] = b
	d2.mu.Unlock()
	c2.add(time.Hour)
	if _, ok := d2.Authenticate(ip, secret); ok {
		t.Fatal("authenticated expired sandbox")
	}

	d3, _, _ := setupEditor(t)
	editorBox := create(t, d3)
	if _, err := d3.StartEditor(context.Background(), editorBox.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	d3.mu.Lock()
	b = d3.boxes[editorBox.ID]
	editorIP := b.editorIP
	b.editorSecret = sha256.Sum256(secret)
	d3.boxes[editorBox.ID] = b
	d3.mu.Unlock()
	if _, ok := d3.Authenticate(editorIP, secret); !ok {
		t.Fatal("editor authentication refused")
	}
	if err := d3.StopEditor(context.Background(), editorBox.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := d3.Authenticate(editorIP, secret); ok {
		t.Fatal("authenticated stopped editor")
	}
}

func TestProxyURLMustBePlainHTTP(t *testing.T) {
	for _, raw := range []string{
		"https://sandboxd:3128",
		"http://user:pw@sandboxd:3128",
		"http://sandboxd:3128/path",
		"http://sandboxd:3128?x=1",
		"http://:3128",
		"sandboxd:3128",
		"://",
	} {
		if _, err := newDriver(newFake(), Options{ProxyURL: raw}); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
	u, err := ParseProxyURL("http://sandboxd:3128/")
	if err != nil || u.String() != "http://sandboxd:3128" {
		t.Fatalf("parse = %v %v", u, err)
	}
}

func TestCreatePullsAMissingImage(t *testing.T) {
	d, f, _ := setup(t)
	s := spec()
	s.Image = "debian:bookworm-slim"
	if _, err := d.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.pulled, []string{"debian:bookworm-slim"}) {
		t.Fatalf("pulled = %v", f.pulled)
	}
}

func TestCreateRefuses(t *testing.T) {
	d, f, _ := setup(t)
	bad := spec()
	bad.Allow = []string{"127.0.0.1"}
	if _, err := d.Create(context.Background(), bad); !errors.Is(err, runtime.ErrSpec) {
		t.Fatalf("spec err = %v", err)
	}
	kata := spec()
	kata.Tier = runtime.TierKata
	if _, err := d.Create(context.Background(), kata); !errors.Is(err, runtime.ErrIsolation) {
		t.Fatalf("kata err = %v, want ErrIsolation", err)
	}
	if len(f.created) != 0 || len(f.pulled) != 0 {
		t.Fatalf("a refused spec reached the engine: created %d pulled %v", len(f.created), f.pulled)
	}
}

func TestCreateGVisorUsesRunsc(t *testing.T) {
	d, f, _ := setup(t)
	s := spec()
	s.Tier = runtime.TierGVisor
	box, err := d.Create(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if box.Tier != runtime.TierGVisor {
		t.Fatalf("tier = %q", box.Tier)
	}
	if len(f.created) != 1 {
		t.Fatalf("created = %d", len(f.created))
	}
	if got := f.created[0].HostConfig.Runtime; got != runtime.RuntimeRunsc {
		t.Fatalf("runtime = %q, want %q", got, runtime.RuntimeRunsc)
	}
}

func TestCreateCleansUpAFailedStart(t *testing.T) {
	d, f, _ := setup(t)
	f.startErr = errors.New("no sleep in image")
	if _, err := d.Create(context.Background(), spec()); err == nil {
		t.Fatal("want an error")
	}
	if len(f.created) != 1 || len(f.removed) != 1 || len(f.volumes) != 1 {
		t.Fatalf("removed %v volumes %v", f.removed, f.volumes)
	}
	if f.removed[0] != f.created[0].Name {
		t.Fatalf("removed %q, created %q", f.removed[0], f.created[0].Name)
	}
}

func TestExecStreamsOutput(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	f.script = []frame{{1, "hello "}, {2, "warn\n"}, {1, "world\n"}}
	f.exit = 3

	var got []runtime.Output
	exit, err := d.Exec(context.Background(), box.ID, runtime.Command{Args: []string{"sh", "-c", "x"}, Dir: "/scratch/repo/", Timeout: 90 * time.Second},
		func(o runtime.Output) error {
			got = append(got, o)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if exit.Code != 3 {
		t.Fatalf("exit = %d", exit.Code)
	}
	want := []runtime.Output{{Stream: "stdout", Data: []byte("hello ")}, {Stream: "stderr", Data: []byte("warn\n")}, {Stream: "stdout", Data: []byte("world\n")}}
	if len(got) != len(want) {
		t.Fatalf("output = %q", got)
	}
	for i := range want {
		if got[i].Stream != want[i].Stream || string(got[i].Data) != string(want[i].Data) {
			t.Fatalf("output[%d] = %s %q", i, got[i].Stream, got[i].Data)
		}
	}
	e := f.execs[0]
	if !slices.Equal(e.Cmd, []string{"timeout", "-s", "KILL", "90", "sh", "-c", "x"}) || e.WorkingDir != "/scratch/repo" {
		t.Fatalf("exec = %+v", e)
	}
	if e.TTY || e.AttachStdin || e.Privileged || e.User != "" {
		t.Fatalf("exec widened: %+v", e)
	}
}

func TestExecDefaultsToTheCap(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	f.script = []frame{}
	if _, err := d.Exec(context.Background(), box.ID, runtime.Command{Args: []string{"true"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.execs[0].Cmd[3]; got != "900" {
		t.Fatalf("timeout = %s, want the 15 minute cap", got)
	}
}

func TestExecHonorsTimeout(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	f.script = nil // never ends on its own

	start := time.Now()
	_, err := d.Exec(context.Background(), box.ID, runtime.Command{Args: []string{"sleep", "60"}, Timeout: 100 * time.Millisecond}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("returned after %v", took)
	}
	if got := f.execs[0].Cmd[3]; got != "1" {
		t.Fatalf("engine-side timeout = %s, want 1", got)
	}
}

func TestExecStopsWhenOutRefuses(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	f.script = []frame{{1, "a"}, {1, "b"}}
	stop := errors.New("enough")
	_, err := d.Exec(context.Background(), box.ID, runtime.Command{Args: []string{"yes"}}, func(runtime.Output) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
}

func TestExecRefuses(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	ctx := context.Background()
	for name, cmd := range map[string]runtime.Command{
		"no args":       {},
		"empty program": {Args: []string{""}},
		"nul":           {Args: []string{"echo", "a\x00b"}},
		"relative dir":  {Args: []string{"ls"}, Dir: "repo"},
		"negative":      {Args: []string{"ls"}, Timeout: -time.Second},
		"past the cap":  {Args: []string{"ls"}, Timeout: runtime.ResourceShape().Exec + time.Second},
	} {
		if _, err := d.Exec(ctx, box.ID, cmd, nil); !errors.Is(err, runtime.ErrSpec) {
			t.Errorf("%s: err = %v, want ErrSpec", name, err)
		}
	}
	if _, err := d.Exec(ctx, "nope", runtime.Command{Args: []string{"ls"}}, nil); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("unknown id err = %v", err)
	}
	if len(f.execs) != 0 {
		t.Fatalf("a refused command reached the engine: %v", f.execs)
	}
}

func TestFilesRoundTrip(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	ctx := context.Background()

	if err := d.WriteFile(ctx, box.ID, "notes/a.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	c := f.copied[0]
	if copyDir(c) != "/scratch/notes/" || !c.CopyUIDGID || c.AllowOverwriteDirWithFile {
		t.Fatalf("copy = %+v", c)
	}
	got, err := d.ReadFile(ctx, box.ID, "/scratch/notes/a.txt")
	if err != nil || string(got) != "hi" {
		t.Fatalf("read = %q %v", got, err)
	}

	if _, err := d.ReadFile(ctx, box.ID, "/scratch/none"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file err = %v", err)
	}
	if _, err := d.ReadFile(ctx, box.ID, "."); !errors.Is(err, ErrNotFile) {
		t.Fatalf("directory err = %v", err)
	}
	if err := d.WriteFile(ctx, box.ID, "/missing/x", nil); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing parent err = %v", err)
	}
	for _, p := range []string{"", "/", "dir/", "a\x00b"} {
		if err := d.WriteFile(ctx, box.ID, p, nil); !errors.Is(err, runtime.ErrSpec) {
			t.Errorf("write %q err = %v", p, err)
		}
	}
	if err := d.WriteFile(ctx, box.ID, "big", make([]byte, MaxFile+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large write err = %v", err)
	}
	if _, err := d.ReadFile(ctx, "nope", "a"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("unknown id err = %v", err)
	}
}

func TestPauseKeepsFilesAndStopsCommands(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	ctx := context.Background()
	if err := d.WriteFile(ctx, box.ID, "keep.txt", []byte("kept")); err != nil {
		t.Fatal(err)
	}
	if err := d.Pause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.stopped, []string{containerName(box.ID)}) || len(f.removed) != 0 {
		t.Fatalf("stopped %v removed %v", f.stopped, f.removed)
	}
	if _, _, ok := d.Egress(box.ID); ok {
		t.Fatal("a paused sandbox has egress")
	}
	if _, err := d.Exec(ctx, box.ID, runtime.Command{Args: []string{"ls"}}, nil); !errors.Is(err, ErrPaused) {
		t.Fatalf("exec on paused err = %v", err)
	}
	if got, err := d.ReadFile(ctx, box.ID, "keep.txt"); err != nil || string(got) != "kept" {
		t.Fatalf("read on paused = %q %v", got, err)
	}
	if err := d.Pause(ctx, "nope"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("pause unknown err = %v", err)
	}
}

func TestDestroyRemovesContainerAndScratch(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	ctx := context.Background()
	if err := d.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.removed, []string{containerName(box.ID)}) || !slices.Equal(f.volumes, []string{volumeName(box.ID)}) {
		t.Fatalf("removed %v volumes %v", f.removed, f.volumes)
	}
	if err := d.Destroy(ctx, box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("second destroy err = %v", err)
	}
	if err := d.Pause(ctx, box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("pause after destroy err = %v", err)
	}
}

func TestTTLExpiryAndSweep(t *testing.T) {
	d, f, c := setup(t)
	box := create(t, d)
	live := create(t, d)
	ctx := context.Background()

	c.add(time.Hour)
	if _, _, ok := d.Egress(box.ID); ok {
		t.Fatal("an expired sandbox has egress")
	}
	if err := d.Destroy(ctx, box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("destroy after expiry err = %v", err)
	}

	f.listed = []container.Summary{
		{Labels: map[string]string{labelSandbox: box.ID, labelExpires: c.now().Add(-time.Second).Format(time.RFC3339Nano)}},
		{Labels: map[string]string{labelSandbox: live.ID, labelExpires: c.now().Add(time.Minute).Format(time.RFC3339Nano)}},
		{Labels: map[string]string{labelSandbox: "018f3b2c-7a10-7c4a-8e11-222222222222", labelExpires: "soon"}},
		{Labels: map[string]string{labelSandbox: "not-a-uuid"}},
	}
	n, err := d.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{containerName(box.ID), containerName("018f3b2c-7a10-7c4a-8e11-222222222222")}
	if n != 2 || !slices.Equal(f.removed, want) {
		t.Fatalf("swept %d: %v", n, f.removed)
	}
}

func TestUnbuiltMethods(t *testing.T) {
	d, _, _ := setup(t)
	ctx := context.Background()
	if _, err := d.PTY(ctx, "x", runtime.Size{}); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("pty")
	}
	if _, err := d.Expose(ctx, "x", 3000); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("expose")
	}
}

func TestStartEditorMountsScratchAndRefusesSecond(t *testing.T) {
	d, f, _ := setupEditor(t)
	box := create(t, d)
	ctx := context.Background()

	ep, err := d.StartEditor(ctx, box.ID, "Working")
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "http://127.0.0.1:8084/e/" + box.ID + "/"
	if ep.URL != wantURL {
		t.Fatalf("url = %q, want %q", ep.URL, wantURL)
	}
	if len(f.created) != 2 {
		t.Fatalf("created %d containers", len(f.created))
	}
	editor := f.created[1]
	if editor.Name != editorContainerName(box.ID) || editor.Config.Image != "upmo/editor:dev" {
		t.Fatalf("editor create = %+v", editor)
	}
	if len(editor.HostConfig.Mounts) != 1 || editor.HostConfig.Mounts[0].Source != volumeName(box.ID) ||
		editor.HostConfig.Mounts[0].Target != ScratchDir || !editor.HostConfig.Mounts[0].ReadOnly {
		t.Fatalf("mount = %+v", editor.HostConfig.Mounts)
	}
	if len(editor.HostConfig.PortBindings) != 0 || string(editor.HostConfig.NetworkMode) != sandboxNetworkName(box.ID) {
		t.Fatalf("network = %q ports = %v", editor.HostConfig.NetworkMode, editor.HostConfig.PortBindings)
	}
	if editor.Config.Labels[labelEditorToken] == "" {
		t.Fatal("missing connection token label")
	}
	if !slices.Contains(editor.Config.Cmd, "--connection-token") {
		t.Fatalf("cmd = %v", editor.Config.Cmd)
	}
	ep2, err := d.StartEditor(ctx, box.ID, "Working")
	if err != nil || ep2.URL != ep.URL {
		t.Fatalf("second start = %+v %v, want url %q", ep2, err, ep.URL)
	}
	if len(f.created) != 2 {
		t.Fatalf("second start created another container: %d", len(f.created))
	}
	if err := d.StopEditor(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	ep, err = d.StartEditor(ctx, box.ID, "Verifying")
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL == "" {
		t.Fatal("expected url after checking")
	}
	if len(f.created) != 3 {
		t.Fatalf("created %d after status change", len(f.created))
	}
	if f.created[2].HostConfig.Mounts[0].ReadOnly {
		t.Fatal("Verifying should mount read-write")
	}
}

func TestStartEditorUsesDefaultImage(t *testing.T) {
	d, f, _ := setup(t)
	box := create(t, d)
	if _, err := d.StartEditor(context.Background(), box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	if len(f.created) < 2 {
		t.Fatalf("created %d", len(f.created))
	}
	ed := f.created[len(f.created)-1]
	if ed.Config == nil || ed.Config.Image != DefaultEditorImage {
		t.Fatalf("image = %v, want %s", ed.Config, DefaultEditorImage)
	}
	if !slices.Equal(f.pulled, []string{DefaultEditorImage}) {
		t.Fatalf("pulled = %v, want %s", f.pulled, DefaultEditorImage)
	}
	if !slices.Contains(ed.Config.Cmd, "--connection-token") || slices.Contains(ed.Config.Cmd, "--without-connection-token") {
		t.Fatalf("cmd = %v", ed.Config.Cmd)
	}
}

func TestDefaultEditorImagePinned(t *testing.T) {
	const prefix = "gitpod/openvscode-server:1.92.1@"
	if !strings.HasPrefix(DefaultEditorImage, prefix) {
		t.Fatalf("DefaultEditorImage = %q, want prefix %q", DefaultEditorImage, prefix)
	}
	if !strings.Contains(DefaultEditorImage, "@sha256:") {
		t.Fatalf("DefaultEditorImage = %q, want @sha256:", DefaultEditorImage)
	}
}

func TestStartEditorKeepsConnectionToken(t *testing.T) {
	d, f, _ := setupEditor(t)
	box := create(t, d)
	ctx := context.Background()
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	first := f.containers[editorContainerName(box.ID)].labels[labelEditorToken]
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	second := f.containers[editorContainerName(box.ID)].labels[labelEditorToken]
	if first == "" || first != second {
		t.Fatalf("token = %q -> %q", first, second)
	}
}

func TestEditorRoute(t *testing.T) {
	d, _, _ := setupEditor(t)
	box := create(t, d)
	ctx := context.Background()
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	route, err := d.EditorRoute(ctx, box.ID)
	if err != nil {
		t.Fatal(err)
	}
	if route.ConnToken == "" || route.Upstream == nil || route.Upstream.Host == "" {
		t.Fatalf("route = %+v", route)
	}
	if route.TaskID != spec().Task {
		t.Fatalf("task = %q", route.TaskID)
	}
}

func TestSweepRemovesIdleEditors(t *testing.T) {
	d, f, c := setupEditor(t)
	box := create(t, d)
	ctx := context.Background()
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	name := editorContainerName(box.ID)
	n, err := d.Sweep(ctx)
	if err != nil || n != 0 {
		t.Fatalf("sweep recent = %d %v", n, err)
	}
	if _, ok := f.containers[name]; !ok {
		t.Fatal("editor removed early")
	}
	c.add(runtime.EditorIdle)
	n, err = d.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep idle = %d %v, want 1", n, err)
	}
	if !slices.Contains(f.removed, name) {
		t.Fatalf("removed %v", f.removed)
	}
}

func TestSweepRemovesEditorWithMissingTouched(t *testing.T) {
	d, f, c := setupEditor(t)
	box := create(t, d)
	ctx := context.Background()
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	name := editorContainerName(box.ID)
	f.mu.Lock()
	delete(f.containers[name].labels, runtime.EditorTouchedLabel)
	f.mu.Unlock()
	c.add(time.Minute)
	n, err := d.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep missing touched = %d %v", n, err)
	}
}

func TestStartEditorRefreshesTouched(t *testing.T) {
	d, f, c := setupEditor(t)
	box := create(t, d)
	ctx := context.Background()
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	name := editorContainerName(box.ID)
	first := f.containers[name].labels[runtime.EditorTouchedLabel]
	c.add(runtime.EditorIdle - time.Minute)
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	second := f.containers[name].labels[runtime.EditorTouchedLabel]
	if second == first || second == "" {
		t.Fatalf("touched = %q -> %q", first, second)
	}
	c.add(runtime.EditorIdle - time.Minute)
	n, err := d.Sweep(ctx)
	if err != nil || n != 0 {
		t.Fatalf("sweep after refresh = %d %v", n, err)
	}
}

func TestStopEditorAndDestroyRemoveEditor(t *testing.T) {
	d, f, _ := setupEditor(t)
	box := create(t, d)
	ctx := context.Background()
	if _, err := d.StartEditor(ctx, box.ID, "Done"); err != nil {
		t.Fatal(err)
	}
	name := editorContainerName(box.ID)
	if err := d.StopEditor(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.removed, name) {
		t.Fatalf("removed %v", f.removed)
	}
	if err := d.StopEditor(ctx, box.ID); err != nil {
		t.Fatal("second stop should be idempotent")
	}
	if _, err := d.StartEditor(ctx, box.ID, "Done"); err != nil {
		t.Fatal(err)
	}
	if err := d.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	want := []string{name, name, containerName(box.ID)}
	if !slices.Equal(f.removed, want) {
		t.Fatalf("removed %v want %v", f.removed, want)
	}
}

func TestStartEditorProxyUser(t *testing.T) {
	d, f, _ := setupEditor(t)
	box := create(t, d)
	if _, err := d.StartEditor(context.Background(), box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	ed := f.created[len(f.created)-1]
	checkProxyEnv(t, ed.Config.Env, "172.20.0.2")
}

func checkProxyEnv(t *testing.T, env []string, host string) {
	t.Helper()
	if len(env) != 6 {
		t.Fatalf("proxy env = %v", env)
	}
	for i := 0; i < len(env); i += 2 {
		key, raw, ok := strings.Cut(env[i], "=")
		if !ok {
			t.Fatalf("proxy env entry %q", env[i])
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host != net.JoinHostPort(host, "3128") || u.User == nil {
			t.Fatalf("proxy url %q: %v", raw, err)
		}
		user := u.User.Username()
		pass, _ := u.User.Password()
		b, e := base64.RawURLEncoding.DecodeString(pass)
		if user != "sandbox" || e != nil || len(b) != 32 {
			t.Fatalf("proxy user=%q secret length=%d err=%v", user, len(b), e)
		}
		clear(b)
		lower, rawLower, ok := strings.Cut(env[i+1], "=")
		if !ok || strings.ToLower(key) != lower || rawLower != raw {
			t.Fatalf("proxy env pair %q %q", env[i], env[i+1])
		}
	}
}

func TestEditorGalleryEgress(t *testing.T) {
	d, _, _ := setupEditor(t)
	box := create(t, d)
	ctx := context.Background()
	eid := runtime.EditorEgressID(box.ID)
	if _, _, ok := d.Egress(eid); ok {
		t.Fatal("editor egress before start")
	}
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	allow, creds, ok := d.Egress(eid)
	if !ok || len(creds) != 0 || len(allow) != 1 || allow[0] != runtime.GalleryHost {
		t.Fatalf("editor egress = %v %v %t", allow, creds, ok)
	}
	dec, err := runtime.AdmitEgress(runtime.GalleryHost, allow)
	if err != nil || dec.Verdict != runtime.VerdictAllow {
		t.Fatalf("gallery admit = %+v %v", dec, err)
	}
	dec, err = runtime.AdmitEgress("evil.example", allow)
	if err != nil || dec.Verdict != runtime.VerdictRefuse {
		t.Fatalf("evil admit = %+v %v", dec, err)
	}
	sallow, _, sok := d.Egress(box.ID)
	if !sok || !slices.Equal(sallow, spec().Allow) {
		t.Fatalf("sandbox egress = %v %t", sallow, sok)
	}
	if err := d.StopEditor(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := d.Egress(eid); ok {
		t.Fatal("editor egress after stop")
	}
}
