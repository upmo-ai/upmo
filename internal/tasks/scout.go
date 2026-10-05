package tasks

// ScoutJobArgs is the River payload for a proposal scout run.
// Adapters in other contexts enqueue this type through the tasks facade.
type ScoutJobArgs struct {
	UserID   string `json:"user_id"`
	Trigger  string `json:"trigger"`
	SourceID string `json:"source_id"`
}

// Kind is the River job kind.
func (ScoutJobArgs) Kind() string { return "tasks_proposal_scout" }
