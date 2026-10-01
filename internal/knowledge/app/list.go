package app

import (
	"context"

	"github.com/google/uuid"
)

// Folder is one knowledge folder the person can see.
type Folder struct {
	ID   string
	Name string
}

// FolderLister returns folders under row security.
type FolderLister interface {
	ListFolders(ctx context.Context, access Access) ([]Folder, error)
}

// ListFolders validates the caller and returns their folders.
func ListFolders(ctx context.Context, book FolderLister, access Access) ([]Folder, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return nil, ErrAccess
	}
	if book == nil {
		return []Folder{}, nil
	}
	out, err := book.ListFolders(ctx, access)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Folder{}
	}
	return out, nil
}

// Source is one document in a folder, without the body.
type Source struct {
	ID        string
	Title     string
	Revision  int
	UpdatedAt string
}

// SourceDetail is one document including the body.
type SourceDetail struct {
	ID        string
	Title     string
	Body      string
	Revision  int
	UpdatedAt string
}

// SourceLister returns sources in one folder under row security.
type SourceLister interface {
	ListSources(ctx context.Context, access Access, folderID string) ([]Source, error)
	ViewSource(ctx context.Context, access Access, folderID, sourceID string) (SourceDetail, error)
}

// ListSources validates the caller and returns sources in a visible folder.
func ListSources(ctx context.Context, book SourceLister, access Access, folderID string) ([]Source, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return nil, ErrAccess
	}
	if _, err := uuid.Parse(folderID); err != nil {
		return nil, ErrFolder
	}
	if book == nil {
		return []Source{}, nil
	}
	out, err := book.ListSources(ctx, access, folderID)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Source{}
	}
	return out, nil
}

// ViewSource validates the caller and returns one source in a visible folder.
func ViewSource(ctx context.Context, book SourceLister, access Access, folderID, sourceID string) (SourceDetail, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return SourceDetail{}, ErrAccess
	}
	if _, err := uuid.Parse(folderID); err != nil {
		return SourceDetail{}, ErrFolder
	}
	if _, err := uuid.Parse(sourceID); err != nil {
		return SourceDetail{}, ErrFolder
	}
	if book == nil {
		return SourceDetail{}, ErrGone
	}
	return book.ViewSource(ctx, access, folderID, sourceID)
}
