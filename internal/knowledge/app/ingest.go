package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/knowledge/domain"
	"github.com/upmo-ai/upmo/internal/platform/trustfence"
)

const prefixBytes = 200 * domain.TokenBytes

var (
	// ErrTitle is a source with no title.
	ErrTitle = errors.New("knowledge: source needs a title")
	// ErrName is a folder with no name.
	ErrName = errors.New("knowledge: folder needs a name")
	// ErrAccess is a missing person or an empty allow list.
	ErrAccess = errors.New("knowledge: access")
	// ErrGone is a folder the person cannot see.
	ErrGone = errors.New("knowledge: gone")
	// ErrFolder is a folder id that is not a UUID, or a source in another folder.
	ErrFolder = errors.New("knowledge: folder")
	// ErrQuery is a blank search query.
	ErrQuery = errors.New("knowledge: query")
)

// Access is the person and the allow list for this transaction.
// The database still decides what those permissions can see.
// Teams, SharedView and SharedEdit are uuid array literals; empty is "{}".
type Access struct {
	UserID     string
	Allow      string
	Teams      string
	SharedView string
	SharedEdit string
}

// Draft is one source to store. Body is kept verbatim.
type Draft struct {
	FolderID string
	SourceID string
	Title    string
	Body     string
}

// Result is what an ingest did. Skipped means the stored hash already matched.
type Result struct {
	ID       string
	Revision int
	Chunks   int
	Skipped  bool
}

// Summariser proposes a ranking prefix for one chunk. The prefix is not evidence.
type Summariser interface {
	Prefix(ctx context.Context, body string) (string, error)
}

// Embedder embeds prefix and body. It does not see the title.
type Embedder interface {
	Embed(ctx context.Context, texts []string) (model string, vectors [][]float32, err error)
}

// Current is the published source, when there is one.
type Current struct {
	Found    bool
	ID       string
	Revision int
	Hash     [32]byte
}

// Piece is one verbatim chunk and its quarantined prefix.
type Piece struct {
	Body   string
	Prefix string
	Vector []float32
	Model  string
}

// Tx is one ingest transaction.
type Tx interface {
	Current(ctx context.Context, folderID, sourceID string) (Current, error)
	Save(ctx context.Context, folderID, sourceID, title, body string, hash [32]byte, pieces []Piece) (Result, error)
}

// Store runs that transaction as the person in Access.
type Store interface {
	InTx(ctx context.Context, access Access, fn func(Tx) error) error
	CreateFolder(ctx context.Context, access Access, name string) (string, error)
}

// Ingest stores a source, then its chunks. The same hash on a published
// revision does not call the summariser or the embedder. An empty source
// stores nothing.
func Ingest(
	ctx context.Context,
	store Store,
	access Access,
	summariser Summariser,
	embedder Embedder,
	draft Draft,
) (Result, error) {
	if err := checkAccess(access); err != nil {
		return Result{}, err
	}
	if _, err := uuid.Parse(draft.FolderID); err != nil {
		return Result{}, ErrFolder
	}
	if draft.SourceID != "" {
		if _, err := uuid.Parse(draft.SourceID); err != nil {
			return Result{}, ErrFolder
		}
	}
	title := strings.TrimSpace(draft.Title)
	if title == "" {
		return Result{}, ErrTitle
	}
	pieces, err := chunkBodies(draft.Body)
	if err != nil {
		return Result{}, err
	}
	hash := sha256.Sum256([]byte(draft.Body))
	var result Result
	err = store.InTx(ctx, access, func(tx Tx) error {
		if draft.SourceID != "" {
			current, err := tx.Current(ctx, draft.FolderID, draft.SourceID)
			if err != nil {
				return err
			}
			if current.Found && current.Hash == hash && current.Revision > 0 {
				result = Result{ID: current.ID, Revision: current.Revision, Skipped: true}
				return nil
			}
		}
		ready, err := prepare(ctx, summariser, embedder, pieces)
		if err != nil {
			return err
		}
		result, err = tx.Save(ctx, draft.FolderID, draft.SourceID, title, draft.Body, hash, ready)
		return err
	})
	return result, err
}

// CreateFolder opens a private folder owned by the person in access.
func CreateFolder(ctx context.Context, store Store, access Access, name string) (string, error) {
	if err := checkAccess(access); err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrName
	}
	return store.CreateFolder(ctx, access, name)
}

func checkAccess(access Access) error {
	if _, err := uuid.Parse(access.UserID); err != nil || strings.TrimSpace(access.Allow) == "" {
		return ErrAccess
	}
	return nil
}

func chunkBodies(body string) ([]Piece, error) {
	parts, err := domain.Chunk(body)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, domain.ErrEmpty
	}
	pieces := make([]Piece, len(parts))
	for i, part := range parts {
		pieces[i].Body = part
	}
	return pieces, nil
}

func prepare(ctx context.Context, summariser Summariser, embedder Embedder, pieces []Piece) ([]Piece, error) {
	texts := make([]string, len(pieces))
	for i := range pieces {
		if summariser != nil {
			prefix, err := summariser.Prefix(ctx, pieces[i].Body)
			if err != nil {
				prefix = ""
			}
			pieces[i].Prefix = quarantine(prefix)
		}
		texts[i] = embedText(pieces[i].Prefix, pieces[i].Body)
	}
	if embedder == nil {
		return pieces, nil
	}
	model, vectors, err := embedder.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(model) == "" || len(vectors) != len(pieces) {
		return nil, errors.New("knowledge: embed")
	}
	for i, vector := range vectors {
		if err := domain.VectorOK(vector); err != nil {
			return nil, err
		}
		pieces[i].Vector = vector
		pieces[i].Model = model
	}
	return pieces, nil
}

func quarantine(prefix string) string {
	prefix = strings.TrimSpace(trustfence.NeutralizeSlot(prefix))
	if len(prefix) <= prefixBytes {
		return prefix
	}
	prefix = prefix[:prefixBytes]
	for prefix != "" && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}

func embedText(prefix, body string) string {
	if prefix == "" {
		return body
	}
	return prefix + "\n" + body
}
