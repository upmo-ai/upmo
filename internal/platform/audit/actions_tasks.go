package audit

// The task actions. A step of POST /api/tasks/{id} records one action per
// transition: the working cycle moves the task along (task.update), the chat
// line adds a line (task.chat), and the edges into Failed and Done change the
// task's status (task.status_change).
const (
	// TaskCreate opens one task.
	TaskCreate Action = "task.create"
	// TaskUpdate advances or plans a task: the working cycle.
	TaskUpdate Action = "task.update"
	// TaskChat appends one chat line to a task.
	TaskChat Action = "task.chat"
	// TaskStatusChange moves a task into Failed or Done.
	TaskStatusChange Action = "task.status_change"
	// TaskShare replaces one task's share.
	TaskShare Action = "task.share"
	// TaskEditorOpen starts a code editor session beside a task.
	TaskEditorOpen Action = "task.editor_open"
	// TaskRun admits one run of a task.
	TaskRun Action = "task.run"
	// ApprovalApprove settles a pending approval with an approve.
	ApprovalApprove Action = "approval.approve"
	// ApprovalDeny settles a pending approval with a deny.
	ApprovalDeny Action = "approval.deny"
)

func init() {
	register(TaskCreate, "task", classAPIActivity, activityUnknown)
	register(TaskUpdate, "task", classAPIActivity, activityUnknown, "status", "budget")
	register(TaskChat, "line", classAPIActivity, activityUnknown)
	register(TaskStatusChange, "task", classAPIActivity, activityUnknown, "status")
	register(TaskShare, "task", classAPIActivity, activityUnknown, "grants", "visibility")
	register(TaskEditorOpen, "task", classAPIActivity, activityUnknown)
	register(TaskRun, "run", classAPIActivity, activityUnknown, "state")
	register(ApprovalApprove, "approval", classAPIActivity, activityUnknown, "state")
	register(ApprovalDeny, "approval", classAPIActivity, activityUnknown, "state")
}
