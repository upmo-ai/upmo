package audit

func chatRoutes() []entry {
	agents := Route{Todo: "UPM-272"}
	return []entry{
		{pattern: "POST /api/chats", route: Route{Action: ChatCreate}},
		{pattern: "POST /api/chats/{id}", route: Route{Action: ChatMessage}},
		{pattern: "POST /api/chats/{id}/lines/{line}/retry", route: Route{Action: ChatRetry}},
		{pattern: "POST /api/agents", route: agents},
		{pattern: "PATCH /api/agents/{id}", route: agents},
		{pattern: "DELETE /api/agents/{id}", route: agents},
	}
}
