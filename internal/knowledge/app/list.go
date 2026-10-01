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
