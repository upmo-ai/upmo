package audit

func knowledgeRoutes() []entry {
	todo := Route{Todo: "UPM-271"}
	return []entry{
		{pattern: "POST /api/knowledge/folders", route: todo},
		{pattern: "PATCH /api/knowledge/folders/{id}", route: todo},
		{pattern: "DELETE /api/knowledge/folders/{id}", route: todo},
		{pattern: "POST /api/knowledge/folders/{id}/sources", route: todo},
		{pattern: "POST /api/knowledge/folders/{id}/sources/url", route: todo},
		{pattern: "POST /api/knowledge/folders/{id}/sources/file", route: todo},
		{pattern: "POST /api/knowledge/folders/{id}/sources/clip", route: todo},
		{pattern: "DELETE /api/knowledge/folders/{id}/sources/{sourceID}", route: todo},
		{pattern: "POST /api/knowledge/folders/{id}/sources/{sourceID}/reingest", route: todo},
	}
}
