package domain

import (
	"errors"
	"strings"
)

// ErrForbiddenChatTool is returned when a tool on the never-from-chat list
// is registered or invoked in chat.
var ErrForbiddenChatTool = errors.New("chat: tool is forbidden in chat")

// ForbiddenChatToolPrefixes enumerates the 4 domains that may never be
// exposed as chat tools:
//  1. Identity, roles, teams, sessions, passkeys, passwords, API tokens, shares
//  2. Provider accounts, credentials, secrets, routes, price overrides, usage limits, connector grants
//  3. Audit log and sealer
//  4. Workspace policy, provisioning, and decision gates
var ForbiddenChatToolPrefixes = []string{
	// 1. Identity / Roles / Sessions / Tokens / Shares
	"identity_", "member_", "members_", "user_", "users_",
	"role_", "roles_", "team_", "teams_", "session_", "sessions_",
	"passkey_", "passkeys_", "password_", "passwords_",
	"token_", "tokens_", "api_token_", "api_tokens_", "share_", "shares_",
	// 2. Provider / Connector credentials / Gateway admin
	"provider_", "providers_", "credential_", "credentials_",
	"secret_", "secrets_", "route_", "routes_",
	"price_override_", "price_overrides_", "usage_limit_", "usage_limits_",
	"connector_grant_", "connector_grants_", "connector_rule_", "connector_rules_",
	// 3. Audit log
	"audit_",
	// 4. Workspace policy / Provisioning / Decision gates
	"workspace_", "provision_", "decision_gate_", "decision_gates_",
}

// ForbiddenChatToolNames lists exact verb-first tool names across the 4 forbidden domains.
var ForbiddenChatToolNames = map[string]bool{
	// 1. Identity / Roles / Sessions / Tokens
	"invite_member": true, "disable_member": true, "remove_member": true, "list_members": true,
	"set_role": true, "create_role": true, "update_role": true, "delete_role": true, "list_roles": true,
	"create_team": true, "update_team": true, "delete_team": true, "add_team_member": true, "remove_team_member": true,
	"revoke_session": true, "list_sessions": true,
	"add_passkey": true, "remove_passkey": true, "set_password": true, "change_password": true, "remove_password": true,
	"create_token": true, "revoke_token": true, "list_tokens": true,
	// 2. Provider / Connector credentials & Gateway config
	"add_provider": true, "update_provider": true, "delete_provider": true, "remove_provider": true,
	"read_credential": true, "write_credential": true, "get_secret": true, "set_secret": true,
	"create_route": true, "update_route": true, "delete_route": true,
	"set_price_override": true, "clear_price_override": true,
	"set_usage_limit": true, "update_usage_limits": true,
	"put_connector_grant": true, "delete_connector_grant": true, "put_connector_rules": true,
	// 3. Audit log
	"read_audit": true, "list_audit": true, "export_audit": true, "verify_audit": true, "seal_audit": true,
	// 4. Workspace policy & Provisioning
	"update_workspace": true, "set_workspace_policy": true, "set_spend_ceiling": true,
	"provision_workspace": true, "reseal_keys": true,
}

// AllowedDirectChatTools is the closed allowlist of in-process Upmo tools permitted in chat.
// Note: search_history is added in Wave 2 (UPM-335) when its executor is registered.
var AllowedDirectChatTools = map[string]bool{
	"retrieve_output":   true,
	"find_tool":         true,
	"open_lesson":       true,
	"search_memory":     true,
	"search_knowledge":  true,
	"list_tasks":        true,
	"open_task":         true,
	"save_memory":       true,
	"list_agents":       true,
	"create_automation": true,
}

// IsNeverFromChat reports whether toolName falls in the forbidden chat tool domains.
func IsNeverFromChat(toolName string) bool {
	norm := strings.ToLower(strings.TrimSpace(toolName))
	if norm == "" {
		return true
	}
	if ForbiddenChatToolNames[norm] {
		return true
	}
	for _, prefix := range ForbiddenChatToolPrefixes {
		if strings.HasPrefix(norm, prefix) {
			return true
		}
	}
	return false
}
