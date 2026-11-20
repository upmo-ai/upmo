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
	// ProviderUsesRead reads the configuration references before a provider is changed.
	ProviderUsesRead Action = "provider.uses.read"
	// ProviderEnable and ProviderDisable record availability changes.
	ProviderEnable  Action = "provider.enable"
	ProviderDisable Action = "provider.disable"
	// PriceOverrideSet and PriceOverrideDelete record per-account model price overrides.
	PriceOverrideSet    Action = "provider.price_override.set"
	PriceOverrideDelete Action = "provider.price_override.delete"

	// RouteCreate stores one gateway route.
	RouteCreate Action = "route.create"
	// RouteUpdate changes one gateway route's fields or hops.
	RouteUpdate Action = "route.update"
	// RouteShare changes who a gateway route is for.
	RouteShare Action = "route.share"
	// RouteDelete removes one gateway route.
	RouteDelete Action = "route.delete"

	// UsageLimitUpdate sets, changes, or removes one monthly spend limit.
	UsageLimitUpdate Action = "usage_limit.update"
	// UsageWorkspaceView reads or exports everyone's usage, a sensitive read.
	UsageWorkspaceView Action = "usage.workspace.view"
)

func init() {
	register(ProviderCreate, "provider", classAPIActivity, activityUnknown, "provider", "plan_kind", "visibility", "team_ids")
	register(ProviderUpdate, "provider", classAPIActivity, activityUnknown, "provider", "plan_kind", "visibility", "team_ids", "status")
	register(ProviderShare, "provider", classAPIActivity, activityUnknown, "visibility", "team_ids")
	register(ProviderDelete, "provider", classAPIActivity, activityUnknown)
	register(ProviderTest, "provider", classAPIActivity, activityUnknown)
	register(ProviderModelsRead, "provider", classAPIActivity, activityUnknown)
	register(ProviderUsesRead, "provider", classAPIActivity, activityUnknown)
	register(ProviderEnable, "provider", classAPIActivity, activityUnknown, "enabled")
	register(ProviderDisable, "provider", classAPIActivity, activityUnknown, "enabled")
	register(PriceOverrideSet, "provider", classAPIActivity, activityUnknown,
		"model", "input_micro_usd_per_mtok", "output_micro_usd_per_mtok", "cache_read_micro_usd_per_mtok", "cache_write_micro_usd_per_mtok")
	register(PriceOverrideDelete, "provider", classAPIActivity, activityUnknown,
		"model", "input_micro_usd_per_mtok", "output_micro_usd_per_mtok", "cache_read_micro_usd_per_mtok", "cache_write_micro_usd_per_mtok")

	register(RouteCreate, "route", classAPIActivity, activityUnknown, "capability", "decision", "visibility", "team_ids")
	register(RouteUpdate, "route", classAPIActivity, activityUnknown, "capability", "decision", "visibility", "team_ids")
	register(RouteShare, "route", classAPIActivity, activityUnknown, "visibility", "team_ids")
	register(RouteDelete, "route", classAPIActivity, activityUnknown)

	register(UsageLimitUpdate, "usage_limit", classAPIActivity, activityUnknown, "kind", "subject_id", "amount_micro_usd")
	register(UsageWorkspaceView, "usage", classAPIActivity, activityUnknown, "range", "group", "format")
}
