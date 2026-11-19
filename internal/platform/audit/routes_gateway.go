package audit

// exemptService is the coverage of the service-to-service RPC routes: their
// caller is authenticated by signature, and the brain action that made the
// call is the recorded one.
const exemptService = "service-to-service call; the originating brain action is recorded"

func gatewayRoutes() []entry {
	exempt := Route{Exempt: exemptService}
	return []entry{
		{pattern: "POST /api/providers", route: Route{Action: ProviderCreate}},
		{pattern: "POST /api/providers/test", route: Route{Action: ProviderTest}},
		{pattern: "GET /api/providers/{id}/models", route: Route{Action: ProviderModelsRead}},
		{pattern: "PUT /api/providers/{id}", route: Route{Action: ProviderUpdate}},
		{pattern: "DELETE /api/providers/{id}", route: Route{Action: ProviderDelete}},
		{pattern: "POST /api/gateway/routes", route: Route{Action: RouteCreate}},
		{pattern: "PATCH /api/gateway/routes/{id}", route: Route{Action: RouteUpdate}},
		{pattern: "DELETE /api/gateway/routes/{id}", route: Route{Action: RouteDelete}},
		{pattern: "PUT /api/gateway/routes/{id}/share", route: Route{Action: RouteShare}},
		{pattern: "POST /upmo.gateway.v1.Gateway/Chat", route: exempt},
		{pattern: "POST /upmo.gateway.v1.Gateway/ChatStream", route: exempt},
		{pattern: "POST /upmo.gateway.v1.Gateway/Decide", route: exempt},
		{pattern: "POST /upmo.gateway.v1.Gateway/Embed", route: exempt},
	}
}
