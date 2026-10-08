package docker

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

const (
	editorNamePrefix = "upmo-editor-"
	labelEditor      = "ai.upmo.editor"
	editorListenPort = 3000
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
			url, urlErr := d.editorURL(ctx, id)
			if urlErr != nil {
				return runtime.Endpoint{}, urlErr
			}
			return runtime.Endpoint{URL: url}, nil
		}
		return runtime.Endpoint{}, err
	}
	image := d.editorImageRef()
	if err := d.ensureImage(ctx, image); err != nil {
		return runtime.Endpoint{}, err
	}

	port, err := network.ParsePort(fmt.Sprintf("%d/tcp", editorListenPort))
	if err != nil {
		return runtime.Endpoint{}, err
	}
	exposed := network.PortSet{port: struct{}{}}
	hostIP := netip.MustParseAddr("0.0.0.0")
	bindings := network.PortMap{port: []network.PortBinding{{HostIP: hostIP, HostPort: "0"}}}
	labels := map[string]string{
		labelSandbox: id,
		labelEditor:  "1",
	}
	readOnly := mountRule.Mode == domain.ModeReadOnly
	init := true
	opts := client.ContainerCreateOptions{
		Name: editorContainerName(id),
		Config: &container.Config{
			Image:        image,
			WorkingDir:   ScratchDir,
			ExposedPorts: exposed,
			Labels:       labels,
			// Bind on all interfaces inside the container so host publish works;
			// connection token is a later brain slice.
			Cmd: []string{
				"--host", "0.0.0.0",
				"--port", strconv.Itoa(editorListenPort),
				"--without-connection-token",
				"--default-folder", ScratchDir,
			},
		},
		HostConfig: &container.HostConfig{
			CapDrop:       []string{"ALL"},
			CapAdd:        slices.Clone(keepCaps),
			SecurityOpt:   []string{"no-new-privileges:true"},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
			Init:          &init,
			PortBindings:  bindings,
			Mounts: []mount.Mount{{
				Type:     mount.TypeVolume,
				Source:   volumeName(id),
				Target:   ScratchDir,
				ReadOnly: readOnly,
			}},
		},
	}
	if _, err := d.api.ContainerCreate(ctx, opts); err != nil {
		_ = d.removeEditor(ctx, id)
		return runtime.Endpoint{}, fmt.Errorf("docker: create editor: %w", err)
	}
	if _, err := d.api.ContainerStart(ctx, editorContainerName(id), client.ContainerStartOptions{}); err != nil {
		_ = d.removeEditor(ctx, id)
		return runtime.Endpoint{}, fmt.Errorf("docker: start editor: %w", err)
	}
	url, err := d.editorURL(ctx, id)
	if err != nil {
		_ = d.removeEditor(ctx, id)
		return runtime.Endpoint{}, err
	}
	return runtime.Endpoint{URL: url}, nil
}

// StopEditor stops and removes the editor beside the sandbox, if any.
func (d *Driver) StopEditor(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.removeEditor(ctx, strings.TrimSpace(id))
}

func (d *Driver) editorOpen(ctx context.Context, sandboxID string) (int, error) {
	list, err := d.api.ContainerList(ctx, client.ContainerListOptions{
		Filters: client.Filters{}.
			Add("label", labelEditor+"=1").
			Add("label", labelSandbox+"="+sandboxID),
	})
	if err != nil {
		return 0, fmt.Errorf("docker: list editors: %w", err)
	}
	n := 0
	for _, c := range list.Items {
		if c.State == container.StateRunning {
			n++
		}
	}
	return n, nil
}

func (d *Driver) editorURL(ctx context.Context, sandboxID string) (string, error) {
	res, err := d.api.ContainerInspect(ctx, editorContainerName(sandboxID), client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("docker: inspect editor: %w", err)
	}
	port, err := network.ParsePort(fmt.Sprintf("%d/tcp", editorListenPort))
	if err != nil {
		return "", err
	}
	bindings, ok := res.Container.NetworkSettings.Ports[port]
	if !ok || len(bindings) == 0 || bindings[0].HostPort == "" {
		return "", fmt.Errorf("docker: editor port is not published")
	}
	hostPort, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil || hostPort <= 0 {
		return "", fmt.Errorf("docker: editor port %q", bindings[0].HostPort)
	}
	host := bindings[0].HostIP
	if !host.IsValid() || host.IsUnspecified() {
		host = netip.MustParseAddr("127.0.0.1")
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host.String(), strconv.Itoa(hostPort))), nil
}

func (d *Driver) removeEditor(ctx context.Context, sandboxID string) error {
	_, err := d.api.ContainerRemove(ctx, editorContainerName(sandboxID), client.ContainerRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("docker: remove editor: %w", err)
	}
	return nil
}

func editorContainerName(id string) string { return editorNamePrefix + id }
