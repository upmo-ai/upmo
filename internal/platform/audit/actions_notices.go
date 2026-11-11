package audit

// The notice actions.
const (
	// NotificationDismiss hides one notice of one person.
	NotificationDismiss Action = "notification.dismiss"
)

func init() {
	register(NotificationDismiss, "notification", classAPIActivity, activityUnknown)
}
