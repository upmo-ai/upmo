package app

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
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

// Reingest retries a failed or ready source. URL kinds re-fetch; others rechunk
// from the stored body. Pending sources are refused.
func Reingest(
	ctx context.Context,
	book ReingestStore,
	access Access,
	fetcher URLFetcher,
	converter BodyConverter,
	summariser Summariser,
	embedder Embedder,
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
				context.Background(),
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

	case "markdown", "clip", "file":
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

	default:
		return ReingestResult{}, ErrReingest
	}
}
