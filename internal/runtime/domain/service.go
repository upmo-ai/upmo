package domain

import "strings"

const (
	// EditorService names the code editor service.
	EditorService = "editor"
	// BesideService places the editor as a service beside the sandbox on Compose.
	BesideService = "service"
	// BesidePod places the editor in the sandbox pod on Kubernetes.
	BesidePod = "pod"
)

// Service is where a code editor runs beside a sandbox and how it sees the scratch volume.
type Service struct {
	Name     string
	Volume   string
	Mode     string
	Beside   string
	Platform string
}

// PlaceEditor returns the editor service for a platform and task status, and never takes a host path, a privileged flag, or an engine socket.
func PlaceEditor(platform, status string) (Service, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))

	var beside string
	switch platform {
	case PlatformCompose:
		beside = BesideService
	case PlatformKubernetes:
		beside = BesidePod
	default:
		return Service{}, ErrEditor
	}

	mount, err := MountRule(status)
	if err != nil {
		return Service{}, ErrEditor
	}

	return Service{
		Name:     EditorService,
		Volume:   mount.Volume,
		Mode:     mount.Mode,
		Beside:   beside,
		Platform: platform,
	}, nil
}
