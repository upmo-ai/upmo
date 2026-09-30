package artifacts

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Where an artifact is held, and the longest name it may carry.
const (
	StoreVolume = "volume"
	StoreObject = "object"
	NameRunes   = 200
)

// keyBytes is the longest object key, matching the column check.
const keyBytes = 1024

// ErrArtifact is returned for any artifact or owner that breaks the rules.
var (
	ErrArtifact = errors.New("artifacts: artifact")
	ErrGone     = errors.New("artifacts: gone")
)

// Artifact is one file a task produced, held apart from the sandbox.
type Artifact struct {
	Owner string
	Task  string
	Name  string
	Store string
	Key   string
}

// Keep checks and normalises an artifact. Owner and task come back in
// canonical UUID form.
func Keep(owner, task, name, store, key string) (Artifact, error) {
	owner, ok := canonical(owner)
	if !ok {
		return Artifact{}, ErrArtifact
	}
	task, ok = canonical(task)
	if !ok {
		return Artifact{}, ErrArtifact
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > NameRunes {
		return Artifact{}, ErrArtifact
	}
	store = strings.TrimSpace(store)
	if store != StoreVolume && store != StoreObject {
		return Artifact{}, ErrArtifact
	}
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return Artifact{}, ErrArtifact
	}
	return Artifact{Owner: owner, Task: task, Name: name, Store: store, Key: key}, nil
}

// Release is what happens to an artifact when its sandbox goes away: nothing.
// The artifact and its key stay as they were.
func Release(a Artifact) Artifact {
	return a
}

// Erase splits items into those another owner holds and the keys of those
// the given owner holds, in the original order. The caller's slice is left
// untouched.
func Erase(items []Artifact, owner string) (kept []Artifact, keys []string, err error) {
	owner, ok := canonical(owner)
	if !ok {
		return nil, nil, ErrArtifact
	}
	kept = make([]Artifact, 0, len(items))
	keys = make([]string, 0)
	for _, item := range items {
		if item.Owner == owner {
			keys = append(keys, item.Key)
			continue
		}
		kept = append(kept, item)
	}
	return kept, keys, nil
}

func canonical(id string) (string, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

func validKey(key string) bool {
	if len(key) < 1 || len(key) > keyBytes {
		return false
	}
	if strings.HasPrefix(key, "/") || strings.Contains(key, `\`) {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}
