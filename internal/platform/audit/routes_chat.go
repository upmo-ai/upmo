package audit

func chatRoutes() []entry {
	return []entry{
		{pattern: "POST /api/chats", route: Route{Action: ChatCreate}},
		{pattern: "POST /api/chats/{id}", route: Route{Action: ChatMessage}},
		{pattern: "POST /api/chats/{id}/lines/{line}/retry", route: Route{Action: ChatRetry}},
		{pattern: "POST /api/agents", route: Route{Action: AgentCreate}},
		{pattern: "PATCH /api/agents/{id}", route: Route{Action: AgentUpdate}},
		{pattern: "DELETE /api/agents/{id}", route: Route{Action: AgentDelete}},
	}
}
