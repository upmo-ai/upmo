package app

import (
	"context"
	"errors"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxUpload = 32 << 20

var (
	// ErrFile is a missing upload or disallowed extension.
	ErrFile = errors.New("knowledge: file")
	// ErrFileSize is an upload over the 32 MiB cap.
	ErrFileSize = errors.New("knowledge: file size")
)

// FileDraft is one uploaded document to ingest into a folder.
type FileDraft struct {
	FolderID string
	Title    string
	Name     string
	Bytes    []byte
}

// allowedExt is the conversion record allow list.
var allowedExt = map[string]string{
	".pdf":  "application/pdf",
	".md":   "text/markdown",
	".txt":  "text/plain",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".html": "text/html",
	".htm":  "text/html",
}

// IngestFile validates the upload and inserts a pending source. Conversion
// and chunking run later through ProcessFile.
func IngestFile(ctx context.Context, store PendingStore, access Access, draft FileDraft) (PendingResult, error) {
	if err := checkAccess(access); err != nil {
		return PendingResult{}, err
	}
	if store == nil {
		return PendingResult{}, errors.New("knowledge: store")
	}
	if _, err := uuid.Parse(draft.FolderID); err != nil {
		return PendingResult{}, ErrFolder
	}
	name, media, err := checkUpload(draft.Name, draft.Bytes)
	if err != nil {
		return PendingResult{}, err
	}
	_ = media
	title := strings.TrimSpace(draft.Title)
	if title == "" {
		title = strings.TrimSuffix(name, path.Ext(name))
		if title == "" {
			title = name
		}
	}
	id, err := store.CreatePending(ctx, access, Pending{
		FolderID:   draft.FolderID,
		Title:      title,
		Kind:       "file",
		SourceRef:  name,
		Provenance: "curated",
	})
	if err != nil {
		return PendingResult{}, err
	}
	return PendingResult{ID: id, Status: "pending"}, nil
}

// ProcessFile converts uploaded bytes and chunks a pending file source.
func ProcessFile(
	ctx context.Context,
	pending PendingStore,
	store Store,
	access Access,
	converter BodyConverter,
	summariser Summariser,
	embedder Embedder,
	folderID, sourceID, name, title string,
	raw []byte,
) error {
	if err := checkAccess(access); err != nil {
		return err
	}
	if pending == nil || store == nil || converter == nil {
		return errors.New("knowledge: process")
	}
	fail := func(message string) error {
		if err := pending.MarkFailed(ctx, access, sourceID, message); err != nil {
			return err
		}
		return nil
	}
	_, media, err := checkUpload(name, raw)
	if err != nil {
		return fail("upload refused")
	}
	markdown, err := converter.ToMarkdown(ctx, media, raw)
	if err != nil {
		if media == "text/plain" || media == "text/markdown" {
			return fail("conversion failed")
		}
		// Non-text types wait on the sidecar; until it lands they fail closed.
		return fail("conversion failed")
	}
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSuffix(path.Base(name), path.Ext(name))
		if title == "" {
			title = name
		}
	}
	_, err = Ingest(ctx, store, access, summariser, embedder, Draft{
		FolderID: folderID,
		SourceID: sourceID,
		Title:    title,
		Body:     markdown,
	})
	if err != nil {
		return fail("ingest failed")
	}
	return nil
}

func checkUpload(name string, raw []byte) (string, string, error) {
	name = path.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "", "", ErrFile
	}
	if len(raw) == 0 {
		return "", "", ErrFile
	}
	if len(raw) > maxUpload {
		return "", "", ErrFileSize
	}
	ext := strings.ToLower(path.Ext(name))
	media, ok := allowedExt[ext]
	if !ok {
		return "", "", ErrFile
	}
	if (media == "text/plain" || media == "text/markdown") && !utf8.Valid(stripNUL(raw)) {
		return "", "", ErrFile
	}
	return name, media, nil
}

func stripNUL(body []byte) []byte {
	cleaned := make([]byte, 0, len(body))
	for _, b := range body {
		if b != 0 {
			cleaned = append(cleaned, b)
		}
	}
	return cleaned
}
