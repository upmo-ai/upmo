package connectors

import (
	"strings"
	"sync"
)

// Held is a stored pause and the task it paused.
type Held struct {
	Pause
	TaskID string
}

// PauseBook keeps held connector calls in process memory, keyed by pause id.
// The zero value is ready. A nil book holds nothing. It neither mints a token nor dials.
type PauseBook struct {
	mu sync.Mutex
	m  map[string]Held
}

// Put holds the call and stores it with its task. It refuses an id already stored.
func (b *PauseBook) Put(id, user, run, task, tool, reason string) (Held, error) {
	if b == nil {
		return Held{}, ErrPause
	}
	p, err := Hold(id, user, run, tool, reason)
	if err != nil {
		return Held{}, ErrPause
	}
	task = strings.TrimSpace(task)
	t, ok := pauseUUID(task)
	if !ok {
		return Held{}, ErrPause
	}
	for _, s := range []string{p.ID, p.User, p.Run} {
		if v, _ := pauseUUID(s); v == t {
			return Held{}, ErrPause
		}
	}
	h := Held{Pause: p, TaskID: task}
	key, _ := pauseKey(p.ID)

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, taken := b.m[key]; taken {
		return Held{}, ErrPause
	}
	if b.m == nil {
		b.m = map[string]Held{}
	}
	b.m[key] = h
	return h, nil
}

// Get returns the stored pause for id.
func (b *PauseBook) Get(id string) (Held, error) {
	key, ok := pauseKey(id)
	if b == nil || !ok {
		return Held{}, ErrPause
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.m[key]
	if !ok {
		return Held{}, ErrPause
	}
	return h, nil
}

// Take resumes the stored pause for its own user, run and id, then removes it.
// A refused resume leaves the pause stored.
func (b *PauseBook) Take(user, run, id string) (Held, error) {
	key, ok := pauseKey(id)
	if b == nil || !ok {
		return Held{}, ErrPause
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.m[key]
	if !ok {
		return Held{}, ErrPause
	}
	if err := Resume(h.Pause, user, run, id); err != nil {
		return Held{}, ErrPause
	}
	delete(b.m, key)
	return h, nil
}

// pauseKey is the canonical form of a pause id, so one UUID has one entry.
func pauseKey(id string) (string, bool) {
	v, ok := pauseUUID(strings.TrimSpace(id))
	if !ok {
		return "", false
	}
	return v.String(), true
}
