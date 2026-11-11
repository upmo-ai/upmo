package app

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/audit"
)

var (
	// ErrReingest is a source that cannot be reingested in its current state.
	ErrReingest = errors.New("knowledge: reingest")
)

// ReingestResult is the immediate reply for a reingest call.
type ReingestResult struct {
	ID       string
	Status   string
	Revision int
	Chunks   int
	Async    bool
}

// ReingestStore loads a source and can reset it to pending for async retry.
type ReingestStore interface {
	SourceLister
	PendingStore
	Store
}

// FileBlobs opens original upload bytes by relative key.
type FileBlobs interface {
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// Reingest retries a failed or ready source. URL kinds re-fetch; file kinds
// with a blob re-convert; others rechunk from the stored body. Pending
// sources are refused.
func Reingest(
	ctx context.Context,
	book ReingestStore,
	access Access,
	fetcher URLFetcher,
	converter BodyConverter,
	summariser Summariser,
	embedder Embedder,
	blobs FileBlobs,
	folderID, sourceID string,
	runAsync func(func()),
) (ReingestResult, error) {
	if err := checkAccess(access); err != nil {
		return ReingestResult{}, err
	}
	if book == nil {
		return ReingestResult{}, ErrGone
	}
	if _, err := uuid.Parse(folderID); err != nil {
		return ReingestResult{}, ErrFolder
	}
	if _, err := uuid.Parse(sourceID); err != nil {
		return ReingestResult{}, ErrFolder
	}
	source, err := book.ViewSource(ctx, access, folderID, sourceID)
	if err != nil {
		return ReingestResult{}, err
	}
	switch source.Status {
	case "pending":
		return ReingestResult{}, ErrReingest
	case "failed", "ready":
	default:
		return ReingestResult{}, ErrReingest
	}

	switch source.Kind {
	case "url":
		ref := strings.TrimSpace(source.SourceRef)
		if ref == "" {
			return ReingestResult{}, ErrURL
		}
		if err := book.ResetPending(ctx, access, sourceID); err != nil {
			return ReingestResult{}, err
		}
		title := source.Title
		run := runAsync
		if run == nil {
			run = func(fn func()) { go fn() }
		}
		run(func() {
			_ = ProcessURL(
				// The re-fetch runs after the response, so its writes record the
				// system as the actor, with the run credential of the reingest.
				audit.WithRunner(context.Background(), audit.CredentialRun),
				book,
				book,
				access,
				fetcher,
				converter,
				summariser,
				embedder,
				folderID,
				sourceID,
				ref,
				title,
			)
		})
		return ReingestResult{ID: sourceID, Status: "pending", Async: true}, nil

	case "file":
		if strings.TrimSpace(source.BlobKey) != "" {
			return reingestFileBlob(ctx, book, access, converter, summariser, embedder, blobs, folderID, source, runAsync)
		}
		return reingestFromBody(ctx, book, access, summariser, embedder, folderID, sourceID, source)

	case "markdown", "clip":
		return reingestFromBody(ctx, book, access, summariser, embedder, folderID, sourceID, source)

	default:
		return ReingestResult{}, ErrReingest
	}
}

func reingestFromBody(
	ctx context.Context,
	book ReingestStore,
	access Access,
	summariser Summariser,
	embedder Embedder,
	folderID, sourceID string,
	source SourceDetail,
) (ReingestResult, error) {
	body := source.Body
	if strings.TrimSpace(body) == "" {
		return ReingestResult{}, ErrReingest
	}
	result, err := Ingest(ctx, book, access, summariser, embedder, Draft{
		FolderID:   folderID,
		SourceID:   sourceID,
		Title:      source.Title,
		Body:       body,
		Kind:       source.Kind,
		SourceRef:  source.SourceRef,
		Provenance: source.Provenance,
	})
	if err != nil {
		return ReingestResult{}, err
	}
	return ReingestResult{
		ID:       result.ID,
		Status:   "ready",
		Revision: result.Revision,
		Chunks:   result.Chunks,
	}, nil
}

func reingestFileBlob(
	ctx context.Context,
	book ReingestStore,
	access Access,
	converter BodyConverter,
	summariser Summariser,
	embedder Embedder,
	blobs FileBlobs,
	folderID string,
	source SourceDetail,
	runAsync func(func()),
) (ReingestResult, error) {
	if converter == nil || blobs == nil {
		return ReingestResult{}, ErrReingest
	}
	r, err := blobs.Open(ctx, source.BlobKey)
	if err != nil {
		return ReingestResult{}, ErrReingest
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxUpload+1))
	_ = r.Close()
	if err != nil || len(raw) == 0 || len(raw) > maxUpload {
		return ReingestResult{}, ErrReingest
	}
	if err := book.ResetPending(ctx, access, source.ID); err != nil {
		return ReingestResult{}, err
	}
	name := source.SourceRef
	title := source.Title
	sourceID := source.ID
	run := runAsync
	if run == nil {
		run = func(fn func()) { go fn() }
	}
	run(func() {
		_ = ProcessFile(
			// The re-conversion runs after the response, so its writes record
			// the system as the actor, with the run credential of the reingest.
			audit.WithRunner(context.Background(), audit.CredentialRun),
			book,
			book,
			access,
			converter,
			summariser,
			embedder,
			folderID,
			sourceID,
			name,
			title,
			raw,
		)
	})
	return ReingestResult{ID: sourceID, Status: "pending", Async: true}, nil
}
