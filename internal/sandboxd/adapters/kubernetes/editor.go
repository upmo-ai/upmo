package kubernetes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

const (
	editorNamePrefix   = "upmo-editor-"
	labelEditor        = "ai.upmo.editor"
	labelEditorToken   = "ai.upmo.editor-tkn"
	editorContainer    = "editor"
	editorListenPort   = 3000
	scratchPVCSize     = "1Gi"
	// DefaultEditorImage is the code editor when Options.EditorImage is empty.
	DefaultEditorImage = "gitpod/openvscode-server:1.92.1"
)

// StartEditor starts the code editor beside a live sandbox.
func (d *Driver) StartEditor(ctx context.Context, id, status string) (runtime.Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return runtime.Endpoint{}, err
	}
	id, _, ok := d.live(id)
	if !ok {
		return runtime.Endpoint{}, runtime.ErrNotFound
	}
	mountRule, err := domain.MountRule(status)
	if err != nil {
		return runtime.Endpoint{}, err
	}
	open, err := d.editorOpen(ctx, id)
	if err != nil {
		return runtime.Endpoint{}, err
	}
	if err := domain.AdmitEditor(open); err != nil {
		if open > 0 {
			if err := d.touchEditor(ctx, id); err != nil {
				return runtime.Endpoint{}, err
			}
			return runtime.Endpoint{URL: editorGateURL(d.editorBaseURL, id)}, nil
		}
		return runtime.Endpoint{}, err
	}
	sandboxPod, err := d.api.GetPod(ctx, podName(id))
	if err != nil {
		if apierrors.IsNotFound(err) {
			d.forget(id)
			return runtime.Endpoint{}, runtime.ErrNotFound
		}
		return runtime.Endpoint{}, fmt.Errorf("kubernetes: start editor: sandbox pod: %w", err)
	}
	readOnly := mountRule.Mode == domain.ModeReadOnly
	image := d.editorImageRef()
	connToken, err := newEditorConnectionToken()
	if err != nil {
		return runtime.Endpoint{}, err
	}
	labels := map[string]string{
		labelSandbox:              id,
		labelEditor:               "1",
		labelEditorToken:          connToken,
		domain.EditorTouchedLabel: domain.FormatEditorTouched(d.now()),
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   editorPodName(id),
			Labels: labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes: []corev1.Volume{{
				Name: scratchVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: scratchPVCName(id),
					},
				},
			}},
			Containers: []corev1.Container{{
				Name:            editorContainer,
				Image:           image,
				WorkingDir:      ScratchDir,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{
					"--host", "0.0.0.0",
					"--port", strconv.Itoa(editorListenPort),
					"--connection-token", connToken,
					"--default-folder", ScratchDir,
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: boolPtr(false),
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
						Add:  capabilityAdds(),
					},
				},
				Ports: []corev1.ContainerPort{{
					ContainerPort: editorListenPort,
					Protocol:      corev1.ProtocolTCP,
				}},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      scratchVolumeName,
					MountPath: ScratchDir,
					ReadOnly:  readOnly,
				}},
			}},
		},
	}
	if sandboxPod.Spec.RuntimeClassName != nil && *sandboxPod.Spec.RuntimeClassName != "" {
		class := *sandboxPod.Spec.RuntimeClassName
		pod.Spec.RuntimeClassName = &class
	}
	svc := editorService(id, labels)
	if _, err := d.api.CreateService(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
		return runtime.Endpoint{}, fmt.Errorf("kubernetes: create editor service: %w", err)
	}
	if _, err := d.api.CreatePod(ctx, pod); err != nil {
		_ = d.removeEditor(ctx, id)
		return runtime.Endpoint{}, fmt.Errorf("kubernetes: create editor: %w", err)
	}
	return runtime.Endpoint{URL: editorGateURL(d.editorBaseURL, id)}, nil
}

// StopEditor deletes the editor Service and Pod beside the sandbox, if any.
func (d *Driver) StopEditor(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.removeEditor(ctx, id)
}

func (d *Driver) editorImageRef() string {
	if img := strings.TrimSpace(d.editorImage); img != "" {
		return img
	}
	return DefaultEditorImage
}

func (d *Driver) editorOpen(ctx context.Context, sandboxID string) (int, error) {
	pod, err := d.api.GetPod(ctx, editorPodName(sandboxID))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("kubernetes: get editor: %w", err)
	}
	if pod.Status.Phase == corev1.PodRunning {
		return 1, nil
	}
	return 0, nil
}

func (d *Driver) removeEditor(ctx context.Context, sandboxID string) error {
	err := d.api.DeleteService(ctx, editorPodName(sandboxID), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubernetes: delete editor service: %w", err)
	}
	err = d.api.DeletePod(ctx, editorPodName(sandboxID), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubernetes: delete editor pod: %w", err)
	}
	return nil
}

func editorPodName(id string) string { return editorNamePrefix + id }

func (d *Driver) touchEditor(ctx context.Context, sandboxID string) error {
	pod, err := d.api.GetPod(ctx, editorPodName(sandboxID))
	if err != nil {
		return fmt.Errorf("kubernetes: touch editor: %w", err)
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[domain.EditorTouchedLabel] = domain.FormatEditorTouched(d.now())
	if _, err := d.api.UpdatePod(ctx, pod); err != nil {
		return fmt.Errorf("kubernetes: touch editor: %w", err)
	}
	return nil
}

func (d *Driver) sweepIdleEditors(ctx context.Context) (int, error) {
	req, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{labelEditor: "1"},
	})
	if err != nil {
		return 0, err
	}
	list, err := d.api.ListPods(ctx, metav1.ListOptions{LabelSelector: req.String()})
	if err != nil {
		return 0, fmt.Errorf("kubernetes: list editors: %w", err)
	}
	now := d.now().UTC()
	removed := 0
	var errs []error
	for _, pod := range list.Items {
		if pod.Labels[labelEditor] != "1" {
			continue
		}
		stop, err := domain.StopIdleEditor(pod.Labels[domain.EditorTouchedLabel], now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !stop {
			continue
		}
		sandboxID := strings.TrimSpace(pod.Labels[labelSandbox])
		if sandboxID == "" {
			if id, ok := strings.CutPrefix(pod.Name, editorNamePrefix); ok {
				sandboxID = id
			}
		}
		if err := d.removeEditor(ctx, sandboxID); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func scratchPVCName(id string) string { return podName(id) + "-scratch" }

func editorService(sandboxID string, labels map[string]string) *corev1.Service {
	port := int32(editorListenPort)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   editorPodName(sandboxID),
			Labels: labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       "editor",
				Port:       port,
				TargetPort: intstr.FromInt32(port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

func scratchPVC(id string, labels map[string]string) *corev1.PersistentVolumeClaim {
	qty := resource.MustParse(scratchPVCSize)
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   scratchPVCName(id),
			Labels: labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: qty,
				},
			},
		},
	}
}

func newEditorConnectionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
