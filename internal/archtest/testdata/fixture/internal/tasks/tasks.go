// Package tasks is a facade. It may import its own internals.
package tasks

import "example.com/fixture/internal/tasks/app"

var _ = app.Name

const Name = "tasks"
