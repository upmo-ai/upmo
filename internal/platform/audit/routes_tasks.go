package audit

func taskRoutes() []entry {
	todo := Route{Todo: "UPM-271"}
	return []entry{
		{pattern: "POST /api/tasks", route: todo},
		{pattern: "POST /api/tasks/{id}", route: todo},
		{pattern: "POST /api/tasks/{id}/editor", route: todo},
		{pattern: "PUT /api/tasks/{id}/share", route: todo},
		{pattern: "POST /api/approvals/{id}/approve", route: todo},
		{pattern: "POST /api/approvals/{id}/deny", route: todo},
	}
}
