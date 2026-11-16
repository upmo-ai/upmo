package audit

// Provisioning actions driven by the signed control-plane API (/api/v1/provision/*).
const (
	ProvisionMemberCreate    Action = "provision.member_create"
	ProvisionMemberUpdate    Action = "provision.member_update"
	ProvisionMemberDelete    Action = "provision.member_delete"
	ProvisionTeamCreate      Action = "provision.team_create"
	ProvisionTeamUpdate      Action = "provision.team_update"
	ProvisionTeamDelete      Action = "provision.team_delete"
	ProvisionSignInLinkIssue Action = "provision.sign_in_link_issue"
	ProvisionProviderUpsert  Action = "provision.provider_upsert"
	ProvisionQuotaSet        Action = "provision.quota_set"
	ProvisionQuotaClear      Action = "provision.quota_clear"
	ProvisionPackInstall     Action = "provision.pack_install"
)

func init() {
	register(ProvisionMemberCreate, "member", classAccountChange, activityCreate,
		"key_id", "role", "active", "teams")
	register(ProvisionMemberUpdate, "member", classAccountChange, activityUnknown,
		"key_id", "role", "active", "teams", "sessions_ended", "tokens_revoked")
	register(ProvisionMemberDelete, "member", classAccountChange, activityDisable,
		"key_id", "sessions_ended", "tokens_revoked")
	register(ProvisionTeamCreate, "team", classUserAccessManagement, activityUnknown,
		"key_id", "members")
	register(ProvisionTeamUpdate, "team", classUserAccessManagement, activityUnknown,
		"key_id", "members")
	register(ProvisionTeamDelete, "team", classUserAccessManagement, activityUnknown,
		"key_id")
	register(ProvisionSignInLinkIssue, "sign_in_link", classAuthentication, activityUnknown,
		"key_id", "purpose")
	register(ProvisionProviderUpsert, "provider", classAPIActivity, activityUnknown,
		"key_id", "count", "adapters", "plan_kinds", "visibility")
	register(ProvisionQuotaSet, "quota", classAPIActivity, activityUnknown,
		"key_id", "spend_ceiling_micro_usd")
	register(ProvisionQuotaClear, "quota", classAPIActivity, activityUnknown,
		"key_id", "spend_ceiling_micro_usd")
	register(ProvisionPackInstall, "pack", classAPIActivity, activityUnknown,
		"key_id", "pack_id", "pack_version", "agent_slug", "lessons")
}
