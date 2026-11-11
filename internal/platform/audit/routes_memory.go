package audit

func memoryRoutes() []entry {
	todo := Route{Todo: "UPM-271"}
	return []entry{
		{pattern: "PUT /api/personalization/profile", route: todo},
		{pattern: "POST /api/personalization/facts", route: todo},
		{pattern: "PUT /api/personalization/facts/{id}", route: todo},
		{pattern: "DELETE /api/personalization/facts/{id}", route: todo},
	}
}
