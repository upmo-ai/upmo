package artifacts

import (
	"context"
	"io"

	"github.com/google/uuid"
)

// Catalog lists and loads artifact rows under row security.
type Catalog interface {
	ListByTask(ctx context.Context, access Access, taskID string) ([]Record, error)
	Get(ctx context.Context, access Access, id string) (Record, error)
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
	headers, err := Serve(rec.Name, Media(rec.Name))
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
