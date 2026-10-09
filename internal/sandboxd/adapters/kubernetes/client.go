package kubernetes

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// cluster is the Kubernetes API surface the driver uses.
type cluster interface {
	CreatePod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error)
	DeletePod(ctx context.Context, name string, opts metav1.DeleteOptions) error
	GetPod(ctx context.Context, name string) (*corev1.Pod, error)
	ListPods(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error)
	CreatePVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error)
	GetPVC(ctx context.Context, name string) (*corev1.PersistentVolumeClaim, error)
	DeletePVC(ctx context.Context, name string, opts metav1.DeleteOptions) error
	CreateService(ctx context.Context, svc *corev1.Service) (*corev1.Service, error)
	GetService(ctx context.Context, name string) (*corev1.Service, error)
	DeleteService(ctx context.Context, name string, opts metav1.DeleteOptions) error
	Exec(ctx context.Context, podName string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error
}

type kubeClient struct {
	cs     kubernetes.Interface
	cfg    *rest.Config
	ns     string
	execFn func(*rest.Config, string, *rest.Request) (remoteCommand, error)
}

type remoteCommand interface {
	StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error
}

func newKubeClient(cs kubernetes.Interface, cfg *rest.Config, ns string) *kubeClient {
	return &kubeClient{
		cs:  cs,
		cfg: cfg,
		ns:  ns,
		execFn: func(cfg *rest.Config, method string, req *rest.Request) (remoteCommand, error) {
			return remotecommand.NewSPDYExecutor(cfg, method, req.URL())
		},
	}
}

func (k *kubeClient) CreatePod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	return k.cs.CoreV1().Pods(k.ns).Create(ctx, pod, metav1.CreateOptions{})
}

func (k *kubeClient) DeletePod(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return k.cs.CoreV1().Pods(k.ns).Delete(ctx, name, opts)
}

func (k *kubeClient) GetPod(ctx context.Context, name string) (*corev1.Pod, error) {
	return k.cs.CoreV1().Pods(k.ns).Get(ctx, name, metav1.GetOptions{})
}

func (k *kubeClient) ListPods(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
	return k.cs.CoreV1().Pods(k.ns).List(ctx, opts)
}

func (k *kubeClient) CreatePVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error) {
	return k.cs.CoreV1().PersistentVolumeClaims(k.ns).Create(ctx, pvc, metav1.CreateOptions{})
}

func (k *kubeClient) GetPVC(ctx context.Context, name string) (*corev1.PersistentVolumeClaim, error) {
	return k.cs.CoreV1().PersistentVolumeClaims(k.ns).Get(ctx, name, metav1.GetOptions{})
}

func (k *kubeClient) DeletePVC(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return k.cs.CoreV1().PersistentVolumeClaims(k.ns).Delete(ctx, name, opts)
}

func (k *kubeClient) CreateService(ctx context.Context, svc *corev1.Service) (*corev1.Service, error) {
	return k.cs.CoreV1().Services(k.ns).Create(ctx, svc, metav1.CreateOptions{})
}

func (k *kubeClient) GetService(ctx context.Context, name string) (*corev1.Service, error) {
	return k.cs.CoreV1().Services(k.ns).Get(ctx, name, metav1.GetOptions{})
}

func (k *kubeClient) DeleteService(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return k.cs.CoreV1().Services(k.ns).Delete(ctx, name, opts)
}

func (k *kubeClient) Exec(ctx context.Context, podName string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req := k.cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(k.ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   cmd,
			Stdin:     stdin != nil,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil,
			TTY:       false,
		}, schemeParamCodec())
	exec, err := k.execFn(k.cfg, "POST", req)
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
}
