package audit

func taskRoutes() []entry {
	return []entry{
		{pattern: "POST /api/tasks", route: Route{Action: TaskCreate}},
		{pattern: "POST /api/tasks/{id}", route: Route{Action: TaskUpdate}},
		{pattern: "POST /api/tasks/{id}/editor", route: Route{Action: TaskEditorOpen}},
		{pattern: "PUT /api/tasks/{id}/share", route: Route{Action: TaskShare}},
		{pattern: "POST /api/approvals/{id}/approve", route: Route{Action: ApprovalApprove}},
		{pattern: "POST /api/approvals/{id}/deny", route: Route{Action: ApprovalDeny}},
	}
}
