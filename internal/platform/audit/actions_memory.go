package audit

// The memory actions. A profile body and a fact claim are content, so the
// events record the field names alone.
const (
	// ProfileUpdate stores one person's profile text.
	ProfileUpdate Action = "profile.update"
	// FactCreate stores one fact the person typed.
	FactCreate Action = "fact.create"
	// FactUpdate supersedes one fact with a new wording.
	FactUpdate Action = "fact.update"
	// FactDelete marks one fact forgotten.
	FactDelete Action = "fact.delete"
)

func init() {
	register(ProfileUpdate, "profile", classAPIActivity, activityUnknown)
	register(FactCreate, "fact", classAPIActivity, activityUnknown)
	register(FactUpdate, "fact", classAPIActivity, activityUnknown, "superseded_by")
	register(FactDelete, "fact", classAPIActivity, activityUnknown)
}
