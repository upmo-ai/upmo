package domain

import (
	"errors"
	"strings"
)

const (
	// TierContainer is the platform default container runtime, the weakest tier.
	TierContainer = "container"
	// TierGVisor isolates a sandbox with gVisor.
	TierGVisor = "gvisor"
	// TierKata isolates a sandbox with Kata, the strong tier.
	TierKata = "kata"
	// PlatformCompose identifies Compose.
	PlatformCompose = "compose"
	// PlatformKubernetes identifies Kubernetes.
	PlatformKubernetes = "kubernetes"
	// RuntimeClassGVisor names the gVisor Kubernetes RuntimeClass.
	RuntimeClassGVisor = "gvisor"
	// RuntimeClassKata names the Kata Kubernetes RuntimeClass.
	RuntimeClassKata = "kata"
	// RuntimeRunsc names the gVisor runtime on Compose.
	RuntimeRunsc = "runsc"
)

// ErrTier reports an unknown tier or platform, or a platform that cannot meet the tier.
var ErrTier = errors.New("runtime: tier")

// Placement is how a platform runs a sandbox at a tier.
// An empty RuntimeClass or Runtime means the platform default.
type Placement struct {
	Tier         string
	RuntimeClass string
	Runtime      string
}

// Place returns a placement that meets tier on platform, and never names a weaker runtime.
func Place(tier, platform string) (Placement, error) {
	tier = strings.ToLower(strings.TrimSpace(tier))
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform != PlatformCompose && platform != PlatformKubernetes {
		return Placement{}, ErrTier
	}

	kube := platform == PlatformKubernetes
	switch {
	case tier == TierContainer:
		return Placement{Tier: TierContainer}, nil
	case tier == TierGVisor && kube:
		return Placement{Tier: TierGVisor, RuntimeClass: RuntimeClassGVisor}, nil
	case tier == TierGVisor:
		return Placement{Tier: TierGVisor, Runtime: RuntimeRunsc}, nil
	case tier == TierKata && kube:
		return Placement{Tier: TierKata, RuntimeClass: RuntimeClassKata}, nil
	default:
		// An unknown tier is refused, as is kata on Compose, where runsc or the default would be weaker.
		return Placement{}, ErrTier
	}
}
