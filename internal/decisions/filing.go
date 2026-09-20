package decisions

import (
	"context"

	"github.com/google/uuid"
)

// RouteFiling is the decide route that chooses a folder for a source.
const RouteFiling = "folder-filing"

// FileSource accepts a folder filing config when its labelled accuracy meets the bar.
func FileSource(ctx context.Context, book Gates, access Access, hops []Hop, bar int, cases []Labelled, run Runner) error {
	return acceptLabelled(ctx, book, access, RouteFiling, hops, bar, cases, run, validFolderID)
}

func validFolderID(option string) bool {
	_, err := uuid.Parse(option)
	return err == nil
}
