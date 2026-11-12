package audit

func auditRoutes() []entry {
	return []entry{
		// A sensitive read, audited like a change: the guard answers it only
		// for audit:view, and the middleware records its refusals.
		{pattern: "GET /api/audit/events", route: Route{Action: AuditRead}},
	}
}
