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
)

func init() {
	register(ProviderCreate, "provider", classAPIActivity, activityUnknown, "provider", "plan_kind", "visibility", "team_ids")
	register(ProviderUpdate, "provider", classAPIActivity, activityUnknown, "provider", "plan_kind", "visibility", "team_ids", "status")
	register(ProviderShare, "provider", classAPIActivity, activityUnknown, "visibility", "team_ids")
	register(ProviderDelete, "provider", classAPIActivity, activityUnknown)
	register(ProviderTest, "provider", classAPIActivity, activityUnknown)
}
