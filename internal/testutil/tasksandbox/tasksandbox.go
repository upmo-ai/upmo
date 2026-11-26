// Package tasksandbox is a runtime.TaskSandboxStore in memory, for tests that drive
// TaskSandboxes without a database. Production keeps task sandboxes in the runtime schema.
package tasksandbox

import (
	"context"
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// Memory is a TaskSandboxStore in memory. The zero value is ready. An empty sandbox
// id is a pending task sandbox.
type Memory struct {
	mu   sync.Mutex
	rows map[string]string
}

var _ runtime.TaskSandboxStore = (*Memory)(nil)

// Claim records a pending task sandbox, or answers runtime.ErrAlreadyHeld.
func (m *Memory) Claim(_ context.Context, taskID string, _ time.Duration, _ []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.rows[taskID]; taken {
		return runtime.ErrAlreadyHeld
	}
	if m.rows == nil {
		m.rows = map[string]string{}
	}
	m.rows[taskID] = ""
	return nil
}

// Fill records sandboxID on a pending task sandbox.
func (m *Memory) Fill(_ context.Context, taskID, sandboxID string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.rows[taskID]; !ok || id != "" {
		return false, nil
	}
	m.rows[taskID] = sandboxID
	return true, nil
}

// Lookup returns the sandbox id taskID holds.
func (m *Memory) Lookup(_ context.Context, taskID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.rows[taskID]
	return id, ok, nil
}

// Drop forgets the row if it still holds sandboxID.
func (m *Memory) Drop(_ context.Context, taskID, sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.rows[taskID]; ok && id == sandboxID {
		delete(m.rows, taskID)
	}
	return nil
}

// Len is how many task sandboxes are recorded, pending ones included.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}
