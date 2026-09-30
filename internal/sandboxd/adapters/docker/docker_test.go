package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"iter"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/mount"
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

	startErr error
	// script is the multiplexed output and exit code of the next exec. A nil
	// script holds the stream open until the driver closes it.
	script []frame
	exit   int
	conns  []net.Conn
}

type frame struct {
	stream byte
	data   string
}

func newFake() *fake {
	return &fake{images: map[string]bool{"upmo/sandbox:base": true}, files: map[string][]byte{}}
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
	return client.ContainerCreateResult{ID: "c-" + opts.Name}, nil
}

func (f *fake) ContainerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
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
	if !opts.Force || !opts.RemoveVolumes {
		return client.ContainerRemoveResult{}, errors.New("remove without force and volumes")
	}
	f.removed = append(f.removed, id)
	return client.ContainerRemoveResult{}, nil
}

func (f *fake) ContainerList(_ context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !opts.All || !opts.Filters["label"][labelSandbox] {
		return client.ContainerListResult{}, errors.New("list without the sandbox label")
	}
	return client.ContainerListResult{Items: slices.Clone(f.listed)}, nil
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
		Credentials: []runtime.Credential{{Sentinel: "upmo_sentinel_gh", Ref: "env:GITHUB_TOKEN", Host: "github.com"}},
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
	f := newFake()
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	return newDriver(f, Options{Now: c.now}), f, c
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

	// No network at all: the only route out is the sandboxd proxy.
	if host.NetworkMode != "none" || !cfg.NetworkDisabled || opts.NetworkingConfig != nil || len(cfg.ExposedPorts) != 0 || len(host.PortBindings) != 0 {
		t.Fatalf("network = %q disabled=%v", host.NetworkMode, cfg.NetworkDisabled)
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
	for _, tier := range []string{runtime.TierGVisor, runtime.TierKata} {
		s := spec()
		s.Tier = tier
		if _, err := d.Create(context.Background(), s); !errors.Is(err, runtime.ErrIsolation) {
			t.Fatalf("%s err = %v, want ErrIsolation", tier, err)
		}
	}
	if len(f.created) != 0 || len(f.pulled) != 0 {
		t.Fatalf("a refused spec reached the engine: created %d pulled %v", len(f.created), f.pulled)
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

func TestExecHonoursTimeout(t *testing.T) {
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
