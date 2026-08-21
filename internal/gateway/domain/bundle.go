package domain

import "fmt"

// Bundle is a named tool surface. A turn uses a bundle, not the full catalog
// (UPM-27).
type Bundle struct {
	Name  string
	Tools []string
}

// ResolveAllow is the allow-list for one turn. When useBundle is true the
// bundle is the surface, even if it is empty. An extra allow-list, if
// present, intersects and keeps bundle order.
func ResolveAllow(bundle, allow []string, useBundle bool) ([]string, error) {
	if !useBundle {
		return allow, nil
	}
	seen := map[string]bool{}
	for _, name := range bundle {
		if name == "" {
			return nil, fmt.Errorf("gateway: bundle tool name is empty")
		}
		if seen[name] {
			return nil, fmt.Errorf("gateway: bundle tool %q repeated", name)
		}
		seen[name] = true
	}
	if len(allow) == 0 {
		out := make([]string, len(bundle))
		copy(out, bundle)
		return out, nil
	}
	keep := map[string]bool{}
	for _, name := range allow {
		if name == "" {
			return nil, fmt.Errorf("gateway: allowed tool name is empty")
		}
		keep[name] = true
	}
	var out []string
	for _, name := range bundle {
		if keep[name] {
			out = append(out, name)
		}
	}
	return out, nil
}
