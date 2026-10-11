package kubernetes

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/editorgate"
)

// EditorRoute resolves the editor upstream for the access gate.
func (d *Driver) EditorRoute(ctx context.Context, sandboxID string) (editorgate.Route, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return editorgate.Route{}, runtime.ErrNotFound
	}
	if _, _, ok := d.live(sandboxID); !ok {
		return editorgate.Route{}, runtime.ErrNotFound
	}
	open, err := d.editorOpen(ctx, sandboxID)
	if err != nil {
		return editorgate.Route{}, err
	}
	if open == 0 {
		return editorgate.Route{}, runtime.ErrNotFound
	}
	pod, err := d.api.GetPod(ctx, editorPodName(sandboxID))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return editorgate.Route{}, runtime.ErrNotFound
		}
		return editorgate.Route{}, fmt.Errorf("kubernetes: get editor: %w", err)
	}
	token := strings.TrimSpace(pod.Labels[labelEditorToken])
	if token == "" {
		return editorgate.Route{}, fmt.Errorf("kubernetes: editor connection token missing")
	}
	raw := editorServiceURL(d.ns, sandboxID)
	upstream, err := url.Parse(raw)
	if err != nil {
		return editorgate.Route{}, err
	}
	taskID := d.sandboxTaskLabel(ctx, sandboxID)
	return editorgate.Route{Upstream: upstream, ConnToken: token, TaskID: taskID}, nil
}

func (d *Driver) sandboxTaskLabel(ctx context.Context, sandboxID string) string {
	pod, err := d.api.GetPod(ctx, podName(sandboxID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(pod.Labels[labelTask])
}

func editorGateURL(base, sandboxID string) string {
	return editorgate.PublicURL(base, sandboxID)
}

func editorServiceURL(ns, sandboxID string) string {
	return fmt.Sprintf("http://%s.%s.svc:%d", editorPodName(sandboxID), ns, editorListenPort)
}
