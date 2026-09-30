// Package leak plants a violation: it imports the sandboxd tree outside it.
package leak

import "example.com/fixture/internal/sandboxd"

var _ = sandboxd.Name
