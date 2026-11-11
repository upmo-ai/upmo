package audit

func noticeRoutes() []entry {
	todo := Route{Todo: "UPM-271"}
	return []entry{
		{pattern: "DELETE /api/notifications/{id}", route: todo},
	}
}
