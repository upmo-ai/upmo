package audit

// OCSF class ids for the catalog; the export golden test pins them.
const (
	classAuthentication = 3002
	classAPIActivity    = 6003
)

// activityUnknown is the activity id of classes without a closer match.
const activityUnknown = 0

// The upkeep actions of the audit log itself.
const (
	// AuditPurge drops one expired monthly partition of sealed events,
	// leaving an anchor when it held any.
	AuditPurge Action = "audit.purge"
	// AuditVerify walks sealed events between two positions and reports where
	// the hash chain breaks.
	AuditVerify Action = "audit.verify"
)

func init() {
	register(SystemMigrated, "schema", classAPIActivity, activityUnknown, "versions")
	register(AuditPurge, "partition", classAPIActivity, activityUnknown, "partition", "events", "through_position")
	register(AuditVerify, "chain", classAPIActivity, activityUnknown, "from_position", "through_position", "failed_position")
}
