package memory

import "github.com/upmo-ai/upmo/internal/memory/app"

// ExtractJobArgs is the River payload for one memory extract run.
// Adapters in other contexts enqueue this type through the memory facade.
type ExtractJobArgs = app.ExtractJobArgs
