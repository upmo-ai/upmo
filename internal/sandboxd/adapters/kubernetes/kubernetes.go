// Package kubernetes is the Kubernetes API driver for the container tier.
// Each sandbox is one Pod with an emptyDir scratch volume at
// /scratch, no host mounts and the fixed resource shape.
//
// Open reads kubeconfig from KUBECONFIG or the default location, or the
// in-cluster service account when sandboxd runs inside a cluster.
package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/upmo-ai/upmo/internal/runtime"
)

const (
	// ScratchDir is where the scratch volume is mounted, and where a
	// relative path or a command with no directory starts.
	ScratchDir = "/scratch"

	containerName = "sandbox"
	namePrefix    = "upmo-sandbox-"

	labelSandbox = "ai.upmo.sandbox"
	labelTask    = "ai.upmo.task"
	labelTier    = "ai.upmo.tier"
	labelExpires = "ai.upmo.expires"

	defaultNamespace = "default"
	configTimeout    = 10 * time.Second
)

// ErrPaused reports a command sent to a paused sandbox. Its files can still
// be read and written.
var ErrPaused = errors.New("kubernetes: sandbox is paused")

// Options tune the driver. The zero value is the default.
type Options struct {
	// Namespace holds sandbox Pods; empty means default or UPMO_SANDBOX_NAMESPACE.
	Namespace string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// ConfigLoader builds REST config; nil uses kubeconfig or in-cluster rules.
	ConfigLoader func() (*rest.Config, error)
	// Client builds the cluster API from REST config; nil uses the real client.
	Client func(*rest.Config) (kubernetes.Interface, error)
}

// Driver runs sandboxes as Kubernetes Pods.
type Driver struct {
	api cluster
	ns  string
	now func() time.Time

	mu    sync.Mutex
	boxes map[string]box
}

type box struct {
	spec    runtime.Spec
	expires time.Time
	paused  bool
}

var _ runtime.SandboxProvider = (*Driver)(nil)

// Open connects to the cluster from kubeconfig or in-cluster config and checks
// the API answers.
func Open(ctx context.Context, opts Options) (*Driver, error) {
	load := opts.ConfigLoader
	if load == nil {
		load = loadRESTConfig
	}
	cfg, err := load()
	if err != nil {
		return nil, fmt.Errorf("kubernetes: no kubeconfig or in-cluster config: %w", err)
	}
	build := opts.Client
	if build == nil {
		build = func(cfg *rest.Config) (kubernetes.Interface, error) {
			return kubernetes.NewForConfig(cfg)
		}
	}
	cs, err := build(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: client: %w", err)
	}
	ns := strings.TrimSpace(opts.Namespace)
	if ns == "" {
		ns = strings.TrimSpace(os.Getenv("UPMO_SANDBOX_NAMESPACE"))
	}
	if ns == "" {
		ns = defaultNamespace
	}
	k := newKubeClient(cs, cfg, ns)
	checkCtx, cancel := context.WithTimeout(ctx, configTimeout)
	defer cancel()
	if _, err := k.GetPod(checkCtx, "upmo-sandbox-config-check"); err != nil {
		if !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("kubernetes: api at %s: %w", cfg.Host, err)
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Driver{api: k, ns: ns, now: opts.Now, boxes: map[string]box{}}, nil
}

func loadRESTConfig() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restCfg, err := cfg.ClientConfig()
	if err == nil {
		return restCfg, nil
	}
	inCluster, icErr := rest.InClusterConfig()
	if icErr != nil {
		return nil, errors.Join(err, icErr)
	}
	return inCluster, nil
}

// Create admits spec and starts a Pod with no host mounts and the fixed shape.
func (d *Driver) Create(ctx context.Context, spec runtime.Spec) (runtime.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return runtime.Sandbox{}, err
	}
	spec, err := runtime.CheckSpec(spec)
	if err != nil {
		return runtime.Sandbox{}, err
	}
	if spec.Tier != runtime.TierContainer {
		return runtime.Sandbox{}, runtime.ErrIsolation
	}
	uid, err := uuid.NewV7()
	if err != nil {
		return runtime.Sandbox{}, err
	}
	id := uid.String()
	expires := d.now().UTC().Add(spec.TTL)
	shape := runtime.ResourceShape()
	podLabels := map[string]string{
		labelSandbox: id,
		labelTask:    spec.Task,
		labelTier:    spec.Tier,
		labelExpires: expires.Format(time.RFC3339Nano),
	}
	mem := int64(2 << 30)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   podName(id),
			Labels: podLabels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Hostname:      "sandbox",
			Volumes: []corev1.Volume{{
				Name: scratchVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}},
			Containers: []corev1.Container{{
				Name:            containerName,
				Image:           spec.Image,
				Command:         []string{"sleep", "infinity"},
				WorkingDir:      ScratchDir,
				ImagePullPolicy: corev1.PullIfNotPresent,
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: boolPtr(false),
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
						Add:  capabilityAdds(),
					},
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:              cpuQuantity(shape.Burst),
						corev1.ResourceMemory:           memoryQuantity(mem),
						corev1.ResourceEphemeralStorage: resourceQuantity("1Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    cpuQuantity(shape.CPU),
						corev1.ResourceMemory: memoryQuantity(mem),
					},
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      scratchVolumeName,
					MountPath: ScratchDir,
				}},
			}},
		},
	}
	if _, err := d.api.CreatePod(ctx, pod); err != nil {
		_ = d.api.DeletePod(context.WithoutCancel(ctx), podName(id), metav1.DeleteOptions{})
		return runtime.Sandbox{}, fmt.Errorf("kubernetes: create: %w", err)
	}
	d.mu.Lock()
	d.boxes[id] = box{spec: spec, expires: expires}
	d.mu.Unlock()
	return runtime.Sandbox{ID: id, Tier: spec.Tier, Shape: shape, Expires: expires}, nil
}

const scratchVolumeName = "scratch"

func capabilityAdds() []corev1.Capability {
	return []corev1.Capability{
		"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETGID", "SETUID", "NET_BIND_SERVICE",
	}
}

func boolPtr(b bool) *bool { return &b }

// Pause marks the sandbox paused. The Pod keeps running so scratch files stay.
func (d *Driver) Pause(ctx context.Context, id string) error {
	id, _, ok := d.live(id)
	if !ok {
		return runtime.ErrNotFound
	}
	if _, err := d.api.GetPod(ctx, podName(id)); err != nil {
		if apierrors.IsNotFound(err) {
			d.forget(id)
			return runtime.ErrNotFound
		}
		return fmt.Errorf("kubernetes: pause: %w", err)
	}
	d.mu.Lock()
	if b, ok := d.boxes[id]; ok {
		b.paused = true
		d.boxes[id] = b
	}
	d.mu.Unlock()
	return nil
}

// Destroy deletes the Pod.
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

// Sweep removes every sandbox Pod whose TTL has passed.
func (d *Driver) Sweep(ctx context.Context) (int, error) {
	req, err := labels.NewRequirement(labelSandbox, selection.Exists, nil)
	if err != nil {
		return 0, err
	}
	sel := labels.NewSelector().Add(*req)
	list, err := d.api.ListPods(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return 0, fmt.Errorf("kubernetes: list: %w", err)
	}
	now := d.now().UTC()
	removed := 0
	var errs []error
	for _, pod := range list.Items {
		id := pod.Labels[labelSandbox]
		if _, err := uuid.Parse(id); err != nil {
			continue
		}
		if expires, err := time.Parse(time.RFC3339Nano, pod.Labels[labelExpires]); err == nil && now.Before(expires) {
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

// Egress returns the allow-list and credentials of a live, unpaused sandbox.
func (d *Driver) Egress(id string) ([]string, []runtime.Credential, bool) {
	_, b, ok := d.live(id)
	if !ok || b.paused {
		return nil, nil, false
	}
	return slices.Clone(b.spec.Allow), slices.Clone(b.spec.Credentials), true
}

// PTY is not built in the Kubernetes driver.
func (d *Driver) PTY(context.Context, string, runtime.Size) (io.ReadWriteCloser, error) {
	return nil, runtime.ErrUnsupported
}

// Expose is not built in the Kubernetes driver.
func (d *Driver) Expose(context.Context, string, int) (runtime.Endpoint, error) {
	return runtime.Endpoint{}, runtime.ErrUnsupported
}

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

func (d *Driver) remove(ctx context.Context, id string) error {
	err := d.api.DeletePod(ctx, podName(id), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubernetes: delete: %w", err)
	}
	return nil
}

func podName(id string) string { return namePrefix + id }
