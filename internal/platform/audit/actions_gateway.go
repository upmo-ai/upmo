package audit

// The gateway actions: the provider accounts people add, and the tests of
// their keys. A key, a base URL and an account name are never values; a
// replaced key is the field name alone.
const (
	// ProviderCreate stores one provider account.
	ProviderCreate Action = "provider.create"
	// ProviderUpdate changes one provider account's fields.
	ProviderUpdate Action = "provider.update"
	// ProviderShare changes who a provider account is for.
	ProviderShare Action = "provider.share"
	// ProviderDelete removes one provider account.
	ProviderDelete Action = "provider.delete"
	// ProviderTest tries one provider account's key with a cheap call.
	ProviderTest Action = "provider.test"
	// ProviderModelsRead reads the discovered model list stored for an account.
	ProviderModelsRead Action = "provider.models.read"

	// RouteCreate stores one gateway route.
	RouteCreate Action = "route.create"
	// RouteUpdate changes one gateway route's fields or hops.
	RouteUpdate Action = "route.update"
	// RouteShare changes who a gateway route is for.
	RouteShare Action = "route.share"
	// RouteDelete removes one gateway route.
	RouteDelete Action = "route.delete"
)

func init() {
	register(ProviderCreate, "provider", classAPIActivity, activityUnknown, "provider", "plan_kind", "visibility", "team_ids")
	register(ProviderUpdate, "provider", classAPIActivity, activityUnknown, "provider", "plan_kind", "visibility", "team_ids", "status")
	register(ProviderShare, "provider", classAPIActivity, activityUnknown, "visibility", "team_ids")
	register(ProviderDelete, "provider", classAPIActivity, activityUnknown)
	register(ProviderTest, "provider", classAPIActivity, activityUnknown)
	register(ProviderModelsRead, "provider", classAPIActivity, activityUnknown)

	register(RouteCreate, "route", classAPIActivity, activityUnknown, "capability", "decision", "visibility", "team_ids")
	register(RouteUpdate, "route", classAPIActivity, activityUnknown, "capability", "decision", "visibility", "team_ids")
	register(RouteShare, "route", classAPIActivity, activityUnknown, "visibility", "team_ids")
	register(RouteDelete, "route", classAPIActivity, activityUnknown)
}
