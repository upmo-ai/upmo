package audit

func chatRoutes() []entry {
	chat := Route{Todo: "UPM-271"}
	agents := Route{Todo: "UPM-272"}
	return []entry{
		{pattern: "POST /api/chats", route: chat},
		{pattern: "POST /api/chats/{id}", route: chat},
		{pattern: "POST /api/chats/{id}/lines/{line}/retry", route: chat},
		{pattern: "POST /api/agents", route: agents},
		{pattern: "PATCH /api/agents/{id}", route: agents},
		{pattern: "DELETE /api/agents/{id}", route: agents},
	}
}
