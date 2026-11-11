package audit

// The knowledge actions. Folder and source names are content, so a rename
// records the field name alone.
const (
	// FolderCreate opens one knowledge folder.
	FolderCreate Action = "folder.create"
	// FolderUpdate renames one knowledge folder.
	FolderUpdate Action = "folder.update"
	// FolderDelete removes one knowledge folder and its sources.
	FolderDelete Action = "folder.delete"
	// SourceAdd stores one knowledge source, as a new row or a new revision.
	SourceAdd Action = "source.add"
	// SourceReingest resets one source for a fresh ingest.
	SourceReingest Action = "source.reingest"
	// SourceDelete removes one knowledge source.
	SourceDelete Action = "source.delete"
)

func init() {
	register(FolderCreate, "folder", classAPIActivity, activityUnknown)
	register(FolderUpdate, "folder", classAPIActivity, activityUnknown)
	register(FolderDelete, "folder", classAPIActivity, activityUnknown)
	register(SourceAdd, "source", classAPIActivity, activityUnknown, "kind", "revision")
	register(SourceReingest, "source", classAPIActivity, activityUnknown, "status")
	register(SourceDelete, "source", classAPIActivity, activityUnknown)
}
