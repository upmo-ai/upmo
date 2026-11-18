package artifacts

import "time"

// Record is one artifact row a person may see under row security.
type Record struct {
	ID        string
	Task      string
	Name      string
	Store     string
	Key       string
	Kind      string
	Mime      string
	Size      int64
	CreatedAt time.Time
}
