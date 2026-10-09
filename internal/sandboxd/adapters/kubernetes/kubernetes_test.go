package kubernetes

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/upmo-ai/upmo/internal/runtime"
)

type fakeCluster struct {
	mu sync.Mutex

	pods      map[string]*corev1.Pod
	pvcs      map[string]*corev1.PersistentVolumeClaim
	services  map[string]*corev1.Service
	created   []*corev1.Pod
	pvcsMade  []*corev1.PersistentVolumeClaim
	svcMade   []*corev1.Service
	deleted   []string
	pvcsDel   []string
	svcDel    []string

	execOut   string
	execErr   error
	execCode  int
	execCalls [][]string
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{
		pods:     map[string]*corev1.Pod{},
		pvcs:     map[string]*corev1.PersistentVolumeClaim{},
		services: map[string]*corev1.Service{},
	}
}

func (f *fakeCluster) CreatePod(_ context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pods[pod.Name]; ok {
		return nil, apierrors.NewAlreadyExists(corev1.Resource("pods"), pod.Name)
	}
	clone := pod.DeepCopy()
	clone.Status.Phase = corev1.PodRunning
	f.pods[pod.Name] = clone
	f.created = append(f.created, clone)
	return clone, nil
}

func (f *fakeCluster) CreatePVC(_ context.Context, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pvcs[pvc.Name]; ok {
		return nil, apierrors.NewAlreadyExists(corev1.Resource("persistentvolumeclaims"), pvc.Name)
	}
	clone := pvc.DeepCopy()
	f.pvcs[pvc.Name] = clone
	f.pvcsMade = append(f.pvcsMade, clone)
	return clone, nil
}

func (f *fakeCluster) GetPVC(_ context.Context, name string) (*corev1.PersistentVolumeClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pvc, ok := f.pvcs[name]
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("persistentvolumeclaims"), name)
	}
	return pvc.DeepCopy(), nil
}

func (f *fakeCluster) DeletePVC(_ context.Context, name string, _ metav1.DeleteOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pvcs[name]; !ok {
		return apierrors.NewNotFound(corev1.Resource("persistentvolumeclaims"), name)
	}
	delete(f.pvcs, name)
	f.pvcsDel = append(f.pvcsDel, name)
	return nil
}

func (f *fakeCluster) CreateService(_ context.Context, svc *corev1.Service) (*corev1.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.services[svc.Name]; ok {
		return nil, apierrors.NewAlreadyExists(corev1.Resource("services"), svc.Name)
	}
	clone := svc.DeepCopy()
	f.services[svc.Name] = clone
	f.svcMade = append(f.svcMade, clone)
	return clone, nil
}

func (f *fakeCluster) GetService(_ context.Context, name string) (*corev1.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[name]
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("services"), name)
	}
	return svc.DeepCopy(), nil
}

func (f *fakeCluster) DeleteService(_ context.Context, name string, _ metav1.DeleteOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.services[name]; !ok {
		return apierrors.NewNotFound(corev1.Resource("services"), name)
	}
	delete(f.services, name)
	f.svcDel = append(f.svcDel, name)
	return nil
}

func (f *fakeCluster) DeletePod(_ context.Context, name string, _ metav1.DeleteOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pods[name]; !ok {
		return apierrors.NewNotFound(corev1.Resource("pods"), name)
	}
	delete(f.pods, name)
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeCluster) GetPod(_ context.Context, name string) (*corev1.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pod, ok := f.pods[name]
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("pods"), name)
	}
	return pod.DeepCopy(), nil
}

func (f *fakeCluster) ListPods(_ context.Context, _ metav1.ListOptions) (*corev1.PodList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := make([]corev1.Pod, 0, len(f.pods))
	for _, pod := range f.pods {
		items = append(items, *pod.DeepCopy())
	}
	return &corev1.PodList{Items: items}, nil
}

func (f *fakeCluster) Exec(_ context.Context, _ string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f.mu.Lock()
	f.execCalls = append(f.execCalls, append([]string(nil), cmd...))
	out := f.execOut
	err := f.execErr
	code := f.execCode
	f.mu.Unlock()
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	if out != "" && stdout != nil {
		_, _ = io.WriteString(stdout, out)
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return fakeExit{code: code}
	}
	_ = stderr
	return nil
}

type fakeExit struct{ code int }

func (e fakeExit) Error() string   { return "exec failed" }
func (e fakeExit) ExitStatus() int { return e.code }

func testDriver(t *testing.T, now func() time.Time) (*Driver, *fakeCluster) {
	t.Helper()
	return testDriverEditor(t, now, "")
}

func testDriverEditor(t *testing.T, now func() time.Time, editorImage string) (*Driver, *fakeCluster) {
	t.Helper()
	fake := newFakeCluster()
	d := &Driver{api: fake, ns: "test", now: now, editorImage: editorImage, boxes: map[string]box{}}
	return d, fake
}

func spec() runtime.Spec {
	return runtime.Spec{
		Task:  "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image: "upmo/sandbox:base",
		Tier:  runtime.TierContainer,
		TTL:   time.Hour,
		Allow: []string{"github.com"},
	}
}

func TestOpenNeedsConfig(t *testing.T) {
	_, err := Open(context.Background(), Options{
		ConfigLoader: func() (*rest.Config, error) { return nil, errors.New("no config") },
	})
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestCreateAdmitsContainerTierOnly(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d, fake := testDriver(t, func() time.Time { return now })
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if box.ID == "" || box.Tier != runtime.TierContainer || box.Shape != runtime.ResourceShape() || !box.Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("box = %+v", box)
	}
	if len(fake.pvcsMade) != 1 {
		t.Fatalf("pvcs = %d", len(fake.pvcsMade))
	}
	if fake.pvcsMade[0].Name != scratchPVCName(box.ID) {
		t.Fatalf("pvc name = %q", fake.pvcsMade[0].Name)
	}
	if len(fake.created) != 1 {
		t.Fatalf("created = %d", len(fake.created))
	}
	pod := fake.created[0]
	if pod.Labels[labelSandbox] != box.ID || pod.Spec.Containers[0].Image != spec().Image {
		t.Fatalf("pod = %+v", pod)
	}
	vol := pod.Spec.Volumes[0].PersistentVolumeClaim
	if vol == nil || vol.ClaimName != scratchPVCName(box.ID) {
		t.Fatalf("scratch volume = %+v", pod.Spec.Volumes[0])
	}
	if name := pod.Spec.RuntimeClassName; name != nil {
		t.Fatalf("container RuntimeClassName = %v, want nil", *name)
	}
}

func TestCreateSetsRuntimeClass(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d, fake := testDriver(t, func() time.Time { return now })
	ctx := context.Background()
	cases := []struct {
		tier string
		want string
	}{
		{runtime.TierGVisor, runtime.RuntimeClassGVisor},
		{runtime.TierKata, runtime.RuntimeClassKata},
	}
	for _, c := range cases {
		s := spec()
		s.Tier = c.tier
		box, err := d.Create(ctx, s)
		if err != nil {
			t.Fatalf("%s: %v", c.tier, err)
		}
		if box.Tier != c.tier {
			t.Fatalf("%s tier = %q", c.tier, box.Tier)
		}
		pod := fake.created[len(fake.created)-1]
		if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != c.want {
			t.Fatalf("%s RuntimeClassName = %v, want %q", c.tier, pod.Spec.RuntimeClassName, c.want)
		}
	}
}

func TestDestroyRemovesPod(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d, fake := testDriver(t, func() time.Time { return now })
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != podName(box.ID) {
		t.Fatalf("deleted pods = %v", fake.deleted)
	}
	if len(fake.pvcsDel) != 1 || fake.pvcsDel[0] != scratchPVCName(box.ID) {
		t.Fatalf("deleted pvcs = %v", fake.pvcsDel)
	}
	if err := d.Destroy(ctx, box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("second destroy = %v", err)
	}
}

func TestSweepRemovesExpiredPods(t *testing.T) {
	past := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	d, fake := testDriver(t, func() time.Time { return past })
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.pods[podName(box.ID)].Labels[labelExpires] = past.Add(-time.Hour).Format(time.RFC3339Nano)
	fake.mu.Unlock()
	n, err := d.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sweep removed %d", n)
	}
}

func TestExecStreamsOutput(t *testing.T) {
	now := time.Now()
	d, fake := testDriver(t, func() time.Time { return now })
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	fake.execOut = "hello"
	var lines []runtime.Output
	exit, err := d.Exec(ctx, box.ID, runtime.Command{Args: []string{"echo", "hello"}}, func(o runtime.Output) error {
		lines = append(lines, o)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if exit.Code != 0 || len(lines) != 1 || lines[0].Stream != "stdout" {
		t.Fatalf("exit = %+v lines = %+v", exit, lines)
	}
}

func TestPauseBlocksExec(t *testing.T) {
	d, fake := testDriver(t, time.Now)
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Pause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	freeze := append([]string(nil), fake.execCalls[0]...)
	fake.mu.Unlock()
	if !slices.Equal(freeze, []string{"kill", "-STOP", "-1"}) {
		t.Fatalf("pause freeze = %v", freeze)
	}
	if _, err := d.Exec(ctx, box.ID, runtime.Command{Args: []string{"true"}}, nil); !errors.Is(err, ErrPaused) {
		t.Fatalf("exec err = %v", err)
	}
	if _, _, ok := d.Egress(box.ID); ok {
		t.Fatal("a paused sandbox has egress")
	}
}

func TestPauseKeepsFilesReadable(t *testing.T) {
	d, fake := testDriver(t, time.Now)
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Pause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	fake.execOut = base64.StdEncoding.EncodeToString([]byte("kept"))
	got, err := d.ReadFile(ctx, box.ID, "keep.txt")
	if err != nil || string(got) != "kept" {
		t.Fatalf("read on paused = %q %v", got, err)
	}
}

func TestStartEditorMountsScratchReadOnlyAndIdempotent(t *testing.T) {
	d, fake := testDriverEditor(t, time.Now, "upmo/editor:dev")
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	wantURL := editorServiceURL("test", box.ID)
	ep, err := d.StartEditor(ctx, box.ID, "Working")
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != wantURL {
		t.Fatalf("url = %q, want %q", ep.URL, wantURL)
	}
	if len(fake.svcMade) != 1 || fake.svcMade[0].Name != editorPodName(box.ID) {
		t.Fatalf("service = %+v", fake.svcMade)
	}
	if len(fake.created) != 2 {
		t.Fatalf("pods = %d", len(fake.created))
	}
	editor := fake.created[1]
	if editor.Name != editorPodName(box.ID) || editor.Spec.Containers[0].Image != "upmo/editor:dev" {
		t.Fatalf("editor = %+v", editor)
	}
	mount := editor.Spec.Containers[0].VolumeMounts[0]
	if mount.MountPath != ScratchDir || !mount.ReadOnly {
		t.Fatalf("mount = %+v", mount)
	}
	ep2, err := d.StartEditor(ctx, box.ID, "Working")
	if err != nil || ep2.URL != ep.URL {
		t.Fatalf("second start = %+v %v", ep2, err)
	}
	if len(fake.created) != 2 {
		t.Fatalf("second start created another pod: %d", len(fake.created))
	}
}

func TestStartEditorUsesDefaultImage(t *testing.T) {
	d, fake := testDriver(t, time.Now)
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	editor := fake.created[len(fake.created)-1]
	if editor.Spec.Containers[0].Image != DefaultEditorImage {
		t.Fatalf("image = %q, want %q", editor.Spec.Containers[0].Image, DefaultEditorImage)
	}
	wantCmd := []string{
		"--host", "0.0.0.0",
		"--port", "3000",
		"--without-connection-token",
		"--default-folder", ScratchDir,
	}
	if !slices.Equal(editor.Spec.Containers[0].Command, wantCmd) {
		t.Fatalf("cmd = %v, want %v", editor.Spec.Containers[0].Command, wantCmd)
	}
}

func TestStopEditorRemovesServiceAndPod(t *testing.T) {
	d, fake := testDriver(t, time.Now)
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartEditor(ctx, box.ID, "Done"); err != nil {
		t.Fatal(err)
	}
	name := editorPodName(box.ID)
	if err := d.StopEditor(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fake.svcDel, name) || !slices.Contains(fake.deleted, name) {
		t.Fatalf("deleted svc %v pod %v", fake.svcDel, fake.deleted)
	}
	if err := d.StopEditor(ctx, box.ID); err != nil {
		t.Fatal("second stop should be idempotent")
	}
}

func TestDestroyRemovesPVCEditorAndSandbox(t *testing.T) {
	d, fake := testDriver(t, time.Now)
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartEditor(ctx, box.ID, "Working"); err != nil {
		t.Fatal(err)
	}
	if err := d.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fake.deleted, editorPodName(box.ID)) || !slices.Contains(fake.deleted, podName(box.ID)) {
		t.Fatalf("deleted pods = %v", fake.deleted)
	}
	if !slices.Contains(fake.pvcsDel, scratchPVCName(box.ID)) {
		t.Fatalf("deleted pvcs = %v", fake.pvcsDel)
	}
}

func TestEgress(t *testing.T) {
	d, _ := testDriver(t, time.Now)
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	allow, creds, ok := d.Egress(box.ID)
	if !ok || len(allow) != 1 || allow[0] != "github.com" || len(creds) != 0 {
		t.Fatalf("egress = %v %v %t", allow, creds, ok)
	}
}
