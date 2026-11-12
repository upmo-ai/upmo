package audit

// Route describes the audit coverage of one mutating HTTP route. Exactly one
// field is set: Action for an instrumented route, Exempt for one whose
// originating action is recorded elsewhere.
type Route struct {
	Action Action
	Exempt string
}

// Table maps one ServeMux pattern to its coverage.
type Table map[string]Route

// entry is one route table line: a pattern and its coverage.
type entry struct {
	pattern string
	route   Route
}

// Routes is the coverage of every changing route in the source. Each area
// file contributes its own entries.
func Routes() Table {
	table := Table{"DELETE /api/session": {Action: AuthSignOut}}
	for _, area := range [][]entry{
		identityRoutes(),
		taskRoutes(),
		chatRoutes(),
		knowledgeRoutes(),
		memoryRoutes(),
		noticeRoutes(),
		gatewayRoutes(),
		sandboxdRoutes(),
		auditRoutes(),
	} {
		for _, e := range area {
			table[e.pattern] = e.route
		}
	}
	return table
}
