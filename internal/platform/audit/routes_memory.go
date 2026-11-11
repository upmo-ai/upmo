package audit

func memoryRoutes() []entry {
	return []entry{
		{pattern: "PUT /api/personalization/profile", route: Route{Action: ProfileUpdate}},
		{pattern: "POST /api/personalization/facts", route: Route{Action: FactCreate}},
		{pattern: "PUT /api/personalization/facts/{id}", route: Route{Action: FactUpdate}},
		{pattern: "DELETE /api/personalization/facts/{id}", route: Route{Action: FactDelete}},
	}
}
