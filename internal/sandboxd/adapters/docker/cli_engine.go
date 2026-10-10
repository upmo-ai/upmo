package docker

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// cliEngine wraps the Moby client and adds helpers the driver needs beyond the
// stock Engine API.
type cliEngine struct {
	*client.Client
}

func (c cliEngine) ContainerSetLabels(ctx context.Context, name string, labels map[string]string) error {
	in, err := c.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("docker: inspect editor labels: %w", err)
	}
	cfg := in.Container.Config
	host := in.Container.HostConfig
	if cfg == nil || host == nil {
		return fmt.Errorf("docker: inspect editor: missing config")
	}
	merged := map[string]string{}
	for k, v := range cfg.Labels {
		merged[k] = v
	}
	for k, v := range labels {
		merged[k] = v
	}
	_, _ = c.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: intPtr(stopSeconds)})
	if _, err := c.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("docker: remove editor for label: %w", err)
	}
	cfgOut := containerConfigFromInspect(cfg, merged)
	opts := client.ContainerCreateOptions{
		Name:       name,
		Config:     cfgOut,
		HostConfig: host,
	}
	if _, err := c.ContainerCreate(ctx, opts); err != nil {
		return fmt.Errorf("docker: recreate editor: %w", err)
	}
	if _, err := c.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("docker: start editor after label: %w", err)
	}
	return nil
}

func containerConfigFromInspect(cfg *container.Config, labels map[string]string) *container.Config {
	if cfg == nil {
		return &container.Config{Labels: labels}
	}
	out := *cfg
	out.Labels = labels
	return &out
}

func intPtr(n int) *int { return &n }
