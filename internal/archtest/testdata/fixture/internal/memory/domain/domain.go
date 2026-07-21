// Package domain plants a transitive violation: it reaches its own app
// through the context facade.
package domain

import "example.com/fixture/internal/memory"

var _ = memory.Name
