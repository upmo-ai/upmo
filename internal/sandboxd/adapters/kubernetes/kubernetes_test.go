package kubernetes

import (
	"context"
	"errors"
	"io"
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

	pods    map[string]*corev1.Pod
	created []*corev1.Pod
	deleted []string

	execOut   string
	execErr   error
	execCode  int
	execCalls [][]string
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{pods: map[string]*corev1.Pod{}}
}

func (f *fakeCluster) CreatePod(_ context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pods[pod.Name]; ok {
		return nil, apierrors.NewAlreadyExists(corev1.Resource("pods"), pod.Name)
	}
	clone := pod.DeepCopy()
	f.pods[pod.Name] = clone
	f.created = append(f.created, clone)
	return clone, nil
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
	fake := newFakeCluster()
	d := &Driver{api: fake, ns: "test", now: now, boxes: map[string]box{}}
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
	if len(fake.created) != 1 {
		t.Fatalf("created = %d", len(fake.created))
	}
	pod := fake.created[0]
	if pod.Labels[labelSandbox] != box.ID || pod.Spec.Containers[0].Image != spec().Image {
		t.Fatalf("pod = %+v", pod)
	}
	gvisor := spec()
	gvisor.Tier = runtime.TierGVisor
	if _, err := d.Create(ctx, gvisor); !errors.Is(err, runtime.ErrIsolation) {
		t.Fatalf("gvisor err = %v", err)
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
		t.Fatalf("deleted = %v", fake.deleted)
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
	d, _ := testDriver(t, time.Now)
	ctx := context.Background()
	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Pause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, box.ID, runtime.Command{Args: []string{"true"}}, nil); !errors.Is(err, ErrPaused) {
		t.Fatalf("exec err = %v", err)
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
