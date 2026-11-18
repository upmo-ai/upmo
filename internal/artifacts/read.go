package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Catalog lists and loads artifact rows under row security.
type Catalog interface {
	ListByTask(ctx context.Context, access Access, taskID string) ([]Record, error)
	Get(ctx context.Context, access Access, id string) (Record, error)
}

// DiffFile is one path in a stored diff. Before is nil when the path was
// created; After is nil when it was deleted. Both nil is not a diff.
type DiffFile struct {
	Path   string  `json:"path"`
	Before *string `json:"before"`
	After  *string `json:"after"`
}

// Diff is the JSON body of a diff artifact. Files is present and non-null,
// so an empty diff encodes as {"files":[]}.
type Diff struct {
	Files []DiffFile `json:"files"`
}

// ListForTask returns the artifacts for a task the person may see.
func ListForTask(ctx context.Context, catalog Catalog, access Access, taskID string) ([]Record, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return nil, ErrArtifact
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return nil, ErrArtifact
	}
	if catalog == nil {
		return nil, ErrGone
	}
	return catalog.ListByTask(ctx, access, taskID)
}

// OpenForDownload loads one artifact and opens its bytes with Serve headers.
func OpenForDownload(ctx context.Context, catalog Catalog, blobs Blobs, access Access, id string) (Record, Headers, io.ReadCloser, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Record{}, Headers{}, nil, ErrArtifact
	}
	if _, err := uuid.Parse(id); err != nil {
		return Record{}, Headers{}, nil, ErrArtifact
	}
	if catalog == nil {
		return Record{}, Headers{}, nil, ErrGone
	}
	rec, err := catalog.Get(ctx, access, id)
	if err != nil {
		return Record{}, Headers{}, nil, err
	}
	media := strings.TrimSpace(rec.Mime)
	if media == "" {
		media = Media(rec.Name)
	}
	headers, err := Serve(rec.Name, media)
	if err != nil {
		return Record{}, Headers{}, nil, err
	}
	if blobs == nil {
		return Record{}, Headers{}, nil, ErrNoBlob
	}
	body, err := blobs.Open(ctx, rec.Store, rec.Key)
	if err != nil {
		return Record{}, Headers{}, nil, err
	}
	return rec, headers, body, nil
}

// OpenForText reads a text-like artifact as UTF-8, capped at MaxViewBytes.
// A stored size over the cap is refused before the blob is opened.
func OpenForText(ctx context.Context, catalog Catalog, blobs Blobs, access Access, id string) (Record, []byte, error) {
	rec, err := loadRecord(ctx, catalog, access, id)
	if err != nil {
		return Record{}, nil, err
	}
	if !IsTextKind(rec.Kind) {
		return Record{}, nil, ErrKind
	}
	raw, err := readView(ctx, blobs, rec)
	if err != nil {
		return Record{}, nil, err
	}
	if !utf8.Valid(raw) {
		return Record{}, nil, ErrKind
	}
	return rec, raw, nil
}

// OpenForDiff reads a diff artifact and checks its JSON. A stored size over
// MaxViewBytes is refused before the blob is opened.
func OpenForDiff(ctx context.Context, catalog Catalog, blobs Blobs, access Access, id string) (Record, Diff, error) {
	rec, err := loadRecord(ctx, catalog, access, id)
	if err != nil {
		return Record{}, Diff{}, err
	}
	if rec.Kind != "diff" {
		return Record{}, Diff{}, ErrKind
	}
	raw, err := readView(ctx, blobs, rec)
	if err != nil {
		return Record{}, Diff{}, err
	}
	diff, err := decodeDiff(raw)
	if err != nil {
		return Record{}, Diff{}, err
	}
	return rec, diff, nil
}

func loadRecord(ctx context.Context, catalog Catalog, access Access, id string) (Record, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Record{}, ErrArtifact
	}
	if _, err := uuid.Parse(id); err != nil {
		return Record{}, ErrArtifact
	}
	if catalog == nil {
		return Record{}, ErrGone
	}
	return catalog.Get(ctx, access, id)
}

// readView enforces the stored size, then reads at most MaxViewBytes+1 so a
// body that grew past the cap is still refused.
func readView(ctx context.Context, blobs Blobs, rec Record) ([]byte, error) {
	if rec.Size > MaxViewBytes {
		return nil, ErrTooLarge
	}
	if blobs == nil {
		return nil, ErrNoBlob
	}
	body, err := blobs.Open(ctx, rec.Store, rec.Key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(body, MaxViewBytes+1))
	if err != nil {
		return nil, fmt.Errorf("artifacts: read: %w", err)
	}
	if int64(len(raw)) > MaxViewBytes {
		return nil, ErrTooLarge
	}
	return raw, nil
}

// decodeDiff parses one JSON value and requires a non-null files array whose
// entries each have a path and at least one side.
func decodeDiff(raw []byte) (Diff, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var diff Diff
	if err := dec.Decode(&diff); err != nil {
		return Diff{}, ErrKind
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return Diff{}, ErrKind
	}
	if diff.Files == nil {
		return Diff{}, ErrKind
	}
	for _, file := range diff.Files {
		if strings.TrimSpace(file.Path) == "" || (file.Before == nil && file.After == nil) {
			return Diff{}, ErrKind
		}
	}
	return diff, nil
}
