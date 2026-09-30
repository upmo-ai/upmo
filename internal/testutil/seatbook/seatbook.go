// Package seatbook is a runtime.SeatBook in memory, for tests that drive
// Seats without a database. Production keeps seats in the runtime schema.
package seatbook

import (
	"context"
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// Memory is a SeatBook in memory. The zero value is ready. An empty sandbox
// id is a pending seat.
type Memory struct {
	mu    sync.Mutex
	seats map[string]string
}

var _ runtime.SeatBook = (*Memory)(nil)

// Claim records a pending seat, or answers runtime.ErrSeated.
func (m *Memory) Claim(_ context.Context, taskID string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.seats[taskID]; taken {
		return runtime.ErrSeated
	}
	if m.seats == nil {
		m.seats = map[string]string{}
	}
	m.seats[taskID] = ""
	return nil
}

// Fill records sandboxID on a pending seat.
func (m *Memory) Fill(_ context.Context, taskID, sandboxID string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.seats[taskID]; !ok || id != "" {
		return false, nil
	}
	m.seats[taskID] = sandboxID
	return true, nil
}

// Seat returns the sandbox id taskID holds.
func (m *Memory) Seat(_ context.Context, taskID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.seats[taskID]
	return id, ok, nil
}

// Drop forgets the seat if it still holds sandboxID.
func (m *Memory) Drop(_ context.Context, taskID, sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.seats[taskID]; ok && id == sandboxID {
		delete(m.seats, taskID)
	}
	return nil
}

// Len is how many seats are recorded, pending ones included.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seats)
}
