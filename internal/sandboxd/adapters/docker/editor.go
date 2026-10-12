package docker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
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
	"github.com/upmo-ai/upmo/internal/sandboxd/editorgate"
)

const (
	editorNamePrefix = "upmo-editor-"
	labelEditor      = "ai.upmo.editor"
	labelEditorToken = "ai.upmo.editor-tkn"
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
			if err := d.touchEditor(ctx, id); err != nil {
				return runtime.Endpoint{}, err
			}
			return runtime.Endpoint{URL: editorgate.PublicURL(d.editorBaseURL, id)}, nil
		}
		return runtime.Endpoint{}, err
	}
	image := d.editorImageRef()
	if err := d.ensureImage(ctx, image); err != nil {
		return runtime.Endpoint{}, err
	}
	connToken, err := newEditorConnectionToken()
	if err != nil {
		return runtime.Endpoint{}, err
	}

	port, err := network.ParsePort(fmt.Sprintf("%d/tcp", editorListenPort))
	if err != nil {
		return runtime.Endpoint{}, err
	}
	exposed := network.PortSet{port: struct{}{}}
	labels := map[string]string{
		labelSandbox:              id,
		labelEditor:               "1",
		labelEditorToken:          connToken,
		domain.EditorTouchedLabel: domain.FormatEditorTouched(d.now()),
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
			Env:          d.proxyEnv(domain.EditorEgressID(id)),
			Cmd: []string{
				"--host", "0.0.0.0",
				"--port", strconv.Itoa(editorListenPort),
				"--connection-token", connToken,
				"--default-folder", ScratchDir,
			},
		},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{d.network: {}},
		},
		HostConfig: &container.HostConfig{
			CapDrop:       []string{"ALL"},
			CapAdd:        slices.Clone(keepCaps),
			SecurityOpt:   []string{"no-new-privileges:true"},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
			Init:          &init,
			NetworkMode:   container.NetworkMode(d.network),
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
	return runtime.Endpoint{URL: editorgate.PublicURL(d.editorBaseURL, id)}, nil
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

func (d *Driver) removeEditor(ctx context.Context, sandboxID string) error {
	_, err := d.api.ContainerRemove(ctx, editorContainerName(sandboxID), client.ContainerRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("docker: remove editor: %w", err)
	}
	return nil
}

func editorContainerName(id string) string { return editorNamePrefix + id }

func (d *Driver) touchEditor(ctx context.Context, sandboxID string) error {
	return d.api.ContainerSetLabels(ctx, editorContainerName(sandboxID), map[string]string{
		domain.EditorTouchedLabel: domain.FormatEditorTouched(d.now()),
	})
}

func (d *Driver) sweepIdleEditors(ctx context.Context) (int, error) {
	list, err := d.api.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", labelEditor+"=1"),
	})
	if err != nil {
		return 0, fmt.Errorf("docker: list editors: %w", err)
	}
	now := d.now().UTC()
	removed := 0
	var errs []error
	for _, c := range list.Items {
		if c.Labels[labelEditor] != "1" {
			continue
		}
		stop, err := domain.StopIdleEditor(c.Labels[domain.EditorTouchedLabel], now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !stop {
			continue
		}
		sandboxID := strings.TrimSpace(c.Labels[labelSandbox])
		if sandboxID == "" {
			for _, raw := range c.Names {
				name := strings.TrimPrefix(raw, "/")
				if id, ok := strings.CutPrefix(name, editorNamePrefix); ok {
					sandboxID = id
					break
				}
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

func newEditorConnectionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
