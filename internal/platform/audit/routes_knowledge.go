package audit

func knowledgeRoutes() []entry {
	return []entry{
		{pattern: "POST /api/knowledge/folders", route: Route{Action: FolderCreate}},
		{pattern: "PATCH /api/knowledge/folders/{id}", route: Route{Action: FolderUpdate}},
		{pattern: "DELETE /api/knowledge/folders/{id}", route: Route{Action: FolderDelete}},
		{pattern: "POST /api/knowledge/folders/{id}/sources", route: Route{Action: SourceAdd}},
		{pattern: "POST /api/knowledge/folders/{id}/sources/url", route: Route{Action: SourceAdd}},
		{pattern: "POST /api/knowledge/folders/{id}/sources/file", route: Route{Action: SourceAdd}},
		{pattern: "POST /api/knowledge/folders/{id}/sources/clip", route: Route{Action: SourceAdd}},
		{pattern: "DELETE /api/knowledge/folders/{id}/sources/{sourceID}", route: Route{Action: SourceDelete}},
		{pattern: "POST /api/knowledge/folders/{id}/sources/{sourceID}/reingest", route: Route{Action: SourceReingest}},
	}
}
