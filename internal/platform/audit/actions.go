package audit

// OCSF class ids for the catalog; the export golden test pins them.
const (
	classAuthentication = 3002
	classAPIActivity    = 6003
)

// activityUnknown is the activity id of classes without a closer match.
const activityUnknown = 0

func init() {
	register(SystemMigrated, "schema", classAPIActivity, activityUnknown, "versions")
}
