package audit

// The read actions of the audit log itself.
const (
	// AuditRead is one signed-in person reading the sealed trail in the app.
	AuditRead Action = "audit.read"
	// AuditExport is a token pulling the sealed trail out, at most once an
	// hour per token.
	AuditExport Action = "audit.export"
)

func init() {
	register(AuditRead, "audit", classAPIActivity, activityUnknown, "filters", "count")
	register(AuditExport, "audit", classAPIActivity, activityUnknown, "count")
}
