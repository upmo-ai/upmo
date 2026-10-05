package jobs

import (
	"context"
	"sync"

	"github.com/riverqueue/river"
)

const pingKind = "platform_ping"

// PingArgs is the test-only ping job payload.
type PingArgs struct {
	Label string `json:"label"`
}

func (PingArgs) Kind() string { return pingKind }

type pingWorker struct {
	river.WorkerDefaults[PingArgs]
}

func (pingWorker) Work(_ context.Context, job *river.Job[PingArgs]) error {
	recordPing(job.Args.Label)
	return nil
}

var (
	pingMu   sync.Mutex
	pingSeen []string
	pingHook func(string)
)

// SetPingHook runs hook from the ping worker during tests. Pass nil to clear.
func SetPingHook(hook func(string)) {
	pingMu.Lock()
	defer pingMu.Unlock()
	pingHook = hook
}

// ResetPing clears recorded ping labels and any hook.
func ResetPing() {
	pingMu.Lock()
	defer pingMu.Unlock()
	pingSeen = nil
	pingHook = nil
}

// PingSeen returns a copy of labels recorded by the ping worker.
func PingSeen() []string {
	pingMu.Lock()
	defer pingMu.Unlock()
	out := make([]string, len(pingSeen))
	copy(out, pingSeen)
	return out
}

func recordPing(label string) {
	pingMu.Lock()
	defer pingMu.Unlock()
	pingSeen = append(pingSeen, label)
	if pingHook != nil {
		pingHook(label)
	}
}
