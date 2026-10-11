package docker

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

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
	res, err := d.api.ContainerInspect(ctx, editorContainerName(sandboxID), client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return editorgate.Route{}, runtime.ErrNotFound
		}
		return editorgate.Route{}, fmt.Errorf("docker: inspect editor: %w", err)
	}
	token := strings.TrimSpace(res.Container.Config.Labels[labelEditorToken])
	if token == "" {
		return editorgate.Route{}, fmt.Errorf("docker: editor connection token missing")
	}
	ip, err := editorNetworkIP(res, d.network)
	if err != nil {
		return editorgate.Route{}, err
	}
	upstream, err := url.Parse(fmt.Sprintf("http://%s", net.JoinHostPort(ip, strconv.Itoa(editorListenPort))))
	if err != nil {
		return editorgate.Route{}, err
	}
	taskID := d.sandboxTaskLabel(ctx, sandboxID)
	return editorgate.Route{Upstream: upstream, ConnToken: token, TaskID: taskID}, nil
}

func (d *Driver) sandboxTaskLabel(ctx context.Context, sandboxID string) string {
	res, err := d.api.ContainerInspect(ctx, containerName(sandboxID), client.ContainerInspectOptions{})
	if err != nil || res.Container.Config == nil {
		return ""
	}
	return strings.TrimSpace(res.Container.Config.Labels[labelTask])
}

func editorNetworkIP(res client.ContainerInspectResult, network string) (string, error) {
	if res.Container.NetworkSettings == nil || res.Container.NetworkSettings.Networks == nil {
		return "", fmt.Errorf("docker: editor has no network settings")
	}
	ep, ok := res.Container.NetworkSettings.Networks[network]
	if !ok || ep == nil || !ep.IPAddress.IsValid() {
		return "", fmt.Errorf("docker: editor is not on network %q", network)
	}
	return ep.IPAddress.String(), nil
}
