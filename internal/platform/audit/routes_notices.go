package audit

func noticeRoutes() []entry {
	return []entry{
		{pattern: "DELETE /api/notifications/{id}", route: Route{Action: NotificationDismiss}},
	}
}
