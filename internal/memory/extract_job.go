package memory

// ExtractJobArgs is the River payload for one memory extract run.
// Adapters in other contexts enqueue this type through the memory facade.
type ExtractJobArgs struct {
	UserID     string `json:"user_id"`
	SourceKind string `json:"source_kind"`
	ID         string `json:"id"`
	Seq        int    `json:"seq"`
	Text       string `json:"text"`
	AtUnix     int64  `json:"at_unix"`
}

// Kind is the River job kind.
func (ExtractJobArgs) Kind() string { return "memory_extract" }
