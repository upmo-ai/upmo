// Package memory plants a violation: it imports another context's domain.
package memory

import (
	"example.com/fixture/internal/memory/app"
	"example.com/fixture/internal/tasks/domain"
)

var (
	_ = app.Name
	_ = domain.Name
)

const Name = "memory"
