package app

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// FolderMutator renames or deletes a folder under row security.
type FolderMutator interface {
	RenameFolder(ctx context.Context, access Access, folderID, name string) error
	DeleteFolder(ctx context.Context, access Access, folderID string) (blobKeys []string, err error)
}

// RenameFolder updates a folder name. Empty names are refused.
func RenameFolder(ctx context.Context, book FolderMutator, access Access, folderID, name string) error {
	if err := checkAccess(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(folderID); err != nil {
		return ErrFolder
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrName
	}
	if book == nil {
		return ErrGone
	}
	return book.RenameFolder(ctx, access, folderID, name)
}

// DeleteFolder removes a folder, cascading sources and chunks. Originals removes
// any file blobs whose keys were returned by the store.
func DeleteFolder(ctx context.Context, book FolderMutator, originals Originals, access Access, folderID string) error {
	if err := checkAccess(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(folderID); err != nil {
		return ErrFolder
	}
	if book == nil {
		return ErrGone
	}
	keys, err := book.DeleteFolder(ctx, access, folderID)
	if err != nil {
		return err
	}
	if originals == nil {
		return nil
	}
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		_ = originals.Remove(ctx, key)
	}
	return nil
}
