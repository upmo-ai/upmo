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
// ErrTooLarge is a text or diff body over MaxViewBytes.
// ErrKind is a kind the view cannot read, or a diff body that is not a diff.
var (
	ErrArtifact = errors.New("artifacts: artifact")
	ErrGone     = errors.New("artifacts: gone")
	ErrTooLarge = errors.New("artifacts: too large")
	ErrKind     = errors.New("artifacts: kind")
)

// Artifact is one file a task produced, held apart from the sandbox.
type Artifact struct {
	Owner string
	Task  string
	Name  string
	Store string
	Key   string
}

// KeepRecord is an artifact plus the metadata stored with it. Kind and Mime
// may be empty; CheckRecord fills them from the name.
type KeepRecord struct {
	Owner string
	Task  string
	Name  string
	Store string
	Key   string
	Kind  string // optional; derived via KindFor(Name) when empty
	Mime  string // optional; derived via Media(Name) when empty
	Size  int64  // >= 0
}

// Keep checks and normalizes an artifact. Owner and task come back in
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

// CheckRecord validates and normalizes a record before it is stored.
// Owner, task, name, store and key go through Keep. An empty kind comes from
// KindFor and an empty mime from Media. A bad kind is ErrKind. A bad mime or
// a negative size is ErrArtifact.
func CheckRecord(in KeepRecord) (KeepRecord, error) {
	a, err := Keep(in.Owner, in.Task, in.Name, in.Store, in.Key)
	if err != nil {
		return KeepRecord{}, err
	}
	media, err := recordMime(in.Mime, a.Name)
	if err != nil {
		return KeepRecord{}, err
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = KindFor(a.Name)
	}
	if !ValidKind(kind) {
		return KeepRecord{}, ErrKind
	}
	if in.Size < 0 {
		return KeepRecord{}, ErrArtifact
	}
	return KeepRecord{
		Owner: a.Owner,
		Task:  a.Task,
		Name:  a.Name,
		Store: a.Store,
		Key:   a.Key,
		Kind:  kind,
		Mime:  media,
		Size:  in.Size,
	}, nil
}

// recordMime trims, lowercases and strips parameters. An empty value takes
// Media(name). The result must be a well-formed type of 1 to 200 characters
// with no CR or LF.
func recordMime(media, name string) (string, error) {
	media = strings.TrimSpace(media)
	if media == "" {
		media = Media(name)
	}
	if strings.ContainsAny(media, "\r\n") {
		return "", ErrArtifact
	}
	media = normalize(media)
	if n := utf8.RuneCountInString(media); n < 1 || n > 200 || !wellFormed(media) {
		return "", ErrArtifact
	}
	return media, nil
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
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
