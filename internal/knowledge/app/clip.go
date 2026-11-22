package app

import (
	"context"
	"crypto/sha256"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxClip = 128 << 10

// ClipDraft is a Pounce page clip: client markdown plus the page URL.
type ClipDraft struct {
	FolderID string
	URL      string
	Title    string
	Markdown string
}

// IngestClip stores a clip synchronously. A second clip with the same
// normalized URL revises the existing clip source.
func IngestClip(
	ctx context.Context,
	store Store,
	access Access,
	summarizer Summarizer,
	embedder Embedder,
	draft ClipDraft,
) (Result, error) {
	if err := checkAccess(access); err != nil {
		return Result{}, err
	}
	if _, err := uuid.Parse(draft.FolderID); err != nil {
		return Result{}, ErrFolder
	}
	ref, err := normalizeClipRef(draft.URL)
	if err != nil {
		return Result{}, err
	}
	body, err := cleanClip(draft.Markdown)
	if err != nil {
		return Result{}, err
	}
	title := strings.TrimSpace(draft.Title)
	if title == "" {
		if u, err := url.Parse(ref); err == nil && u.Host != "" {
			title = u.Host
		} else {
			title = ref
		}
	}
	pieces, err := chunkBodies(body)
	if err != nil {
		return Result{}, err
	}
	hash := sha256.Sum256([]byte(body))
	var result Result
	err = store.InTx(ctx, access, func(tx Tx) error {
		sourceID, folderID, err := tx.FindClip(ctx, ref)
		if err != nil {
			return err
		}
		if sourceID != "" {
			if folderID != draft.FolderID {
				return ErrFolder
			}
			current, err := tx.Current(ctx, draft.FolderID, sourceID)
			if err != nil {
				return err
			}
			if current.Found && current.Hash == hash && current.Revision > 0 {
				result = Result{ID: current.ID, Revision: current.Revision, Skipped: true}
				return nil
			}
		}
		ready, err := prepare(ctx, summarizer, embedder, pieces)
		if err != nil {
			return err
		}
		result, err = tx.Save(ctx, Draft{
			FolderID:   draft.FolderID,
			SourceID:   sourceID,
			Title:      title,
			Body:       body,
			Kind:       "clip",
			SourceRef:  ref,
			Provenance: "web",
		}, hash, ready)
		return err
	})
	return result, err
}

func normalizeClipRef(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", ErrURL
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrURL
	}
	u.Scheme = scheme
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if (scheme == "http" && u.Port() == "80") || (scheme == "https" && u.Port() == "443") {
		u.Host = u.Hostname()
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func cleanClip(markdown string) (string, error) {
	if len(markdown) > maxClip {
		return "", ErrFileSize
	}
	cleaned := strings.Map(func(r rune) rune {
		if r == 0 {
			return -1
		}
		return r
	}, markdown)
	if !utf8.ValidString(cleaned) {
		return "", ErrFile
	}
	if strings.TrimSpace(cleaned) == "" {
		return "", ErrFile
	}
	return cleaned, nil
}
