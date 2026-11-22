package app

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

var (
	// ErrURL is a missing or non-http(s) URL.
	ErrURL = errors.New("knowledge: url")
	// ErrURLBlocked is a loopback or private URL host.
	ErrURLBlocked = errors.New("knowledge: url blocked")
	// ErrConvert is a body the converter could not turn into markdown.
	ErrConvert = errors.New("knowledge: convert")
)

// URLDraft is one URL to ingest into a folder.
type URLDraft struct {
	FolderID string
	URL      string
	Title    string
}

// PendingResult is a source row waiting on fetch or conversion.
type PendingResult struct {
	ID     string
	Status string
}

// Pending is a new source that is not ready yet.
type Pending struct {
	FolderID   string
	Title      string
	Kind       string
	SourceRef  string
	Provenance string
}

// PendingStore creates pending sources and records failures.
type PendingStore interface {
	CreatePending(ctx context.Context, access Access, pending Pending) (string, error)
	MarkFailed(ctx context.Context, access Access, sourceID, message string) error
	ResetPending(ctx context.Context, access Access, sourceID string) error
	SetBlobKey(ctx context.Context, access Access, sourceID, blobKey string) error
}

// URLFetcher loads one public URL. Production dials through netguard.
type URLFetcher interface {
	Fetch(ctx context.Context, rawURL string) (contentType string, body []byte, err error)
}

// BodyConverter turns fetched bytes into a markdown body.
type BodyConverter interface {
	ToMarkdown(ctx context.Context, contentType string, body []byte) (string, error)
}

// IngestURL validates the URL and inserts a pending source. Fetch and
// conversion run later through ProcessURL.
func IngestURL(ctx context.Context, store PendingStore, access Access, draft URLDraft) (PendingResult, error) {
	if err := checkAccess(access); err != nil {
		return PendingResult{}, err
	}
	if store == nil {
		return PendingResult{}, errors.New("knowledge: store")
	}
	if _, err := uuid.Parse(draft.FolderID); err != nil {
		return PendingResult{}, ErrFolder
	}
	ref, title, err := normalizeURL(draft.URL, draft.Title)
	if err != nil {
		return PendingResult{}, err
	}
	id, err := store.CreatePending(ctx, access, Pending{
		FolderID:   draft.FolderID,
		Title:      title,
		Kind:       "url",
		SourceRef:  ref,
		Provenance: "curated",
	})
	if err != nil {
		return PendingResult{}, err
	}
	return PendingResult{ID: id, Status: "pending"}, nil
}

// ProcessURL fetches, converts and chunks a pending URL source. On failure
// it marks the source failed and leaves revision 0 with no chunks.
func ProcessURL(
	ctx context.Context,
	pending PendingStore,
	store Store,
	access Access,
	fetcher URLFetcher,
	converter BodyConverter,
	summarizer Summarizer,
	embedder Embedder,
	folderID, sourceID, rawURL, title string,
) error {
	if err := checkAccess(access); err != nil {
		return err
	}
	if pending == nil || store == nil || fetcher == nil || converter == nil {
		return errors.New("knowledge: process")
	}
	fail := func(message string) error {
		if err := pending.MarkFailed(ctx, access, sourceID, message); err != nil {
			return err
		}
		return nil
	}
	contentType, body, err := fetcher.Fetch(ctx, rawURL)
	if err != nil {
		if errors.Is(err, netguard.ErrBlocked) || errors.Is(err, ErrURLBlocked) {
			return fail("url blocked")
		}
		return fail("fetch failed")
	}
	markdown, err := converter.ToMarkdown(ctx, contentType, body)
	if err != nil {
		return fail("conversion failed")
	}
	if strings.TrimSpace(title) == "" {
		_, title, _ = normalizeURL(rawURL, "")
	}
	_, err = Ingest(ctx, store, access, summarizer, embedder, Draft{
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

// PassThroughText keeps text/plain and text/markdown as the body. Other
// types wait for the conversion sidecar.
type PassThroughText struct{}

// ToMarkdown implements BodyConverter.
func (PassThroughText) ToMarkdown(_ context.Context, contentType string, body []byte) (string, error) {
	media := contentType
	if i := strings.Index(media, ";"); i >= 0 {
		media = media[:i]
	}
	media = strings.TrimSpace(strings.ToLower(media))
	switch media {
	case "text/plain", "text/markdown", "text/x-markdown":
		return cleanText(body)
	default:
		return "", ErrConvert
	}
}

func normalizeURL(raw, title string) (ref, named string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", ErrURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", ErrURL
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", ErrURL
	}
	host := u.Hostname()
	if host == "" {
		return "", "", ErrURL
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", "", ErrURLBlocked
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if reason := netguard.BlockedAddr(ip); reason != "" {
			return "", "", ErrURLBlocked
		}
	}
	ref = u.String()
	named = strings.TrimSpace(title)
	if named == "" {
		named = host
		if path := strings.Trim(u.EscapedPath(), "/"); path != "" {
			if i := strings.LastIndex(path, "/"); i >= 0 {
				path = path[i+1:]
			}
			if path != "" {
				named = path
			}
		}
	}
	return ref, named, nil
}

func cleanText(body []byte) (string, error) {
	if len(body) == 0 {
		return "", ErrConvert
	}
	cleaned := make([]byte, 0, len(body))
	for _, b := range body {
		if b != 0 {
			cleaned = append(cleaned, b)
		}
	}
	if !utf8.Valid(cleaned) {
		return "", ErrConvert
	}
	text := string(cleaned)
	if strings.TrimSpace(text) == "" {
		return "", ErrConvert
	}
	return text, nil
}
