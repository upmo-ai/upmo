package tasks

import "github.com/upmo-ai/upmo/internal/tasks/app"

// ScoutJobArgs is the River payload for a proposal scout run.
// Adapters in other contexts enqueue this type through the tasks facade.
type ScoutJobArgs = app.ScoutJobArgs
