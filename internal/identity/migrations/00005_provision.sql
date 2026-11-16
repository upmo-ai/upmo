-- +goose Up

ALTER TABLE identity.teams
  ADD COLUMN external_id text;

CREATE UNIQUE INDEX teams_external_id ON identity.teams (external_id) WHERE external_id IS NOT NULL;

ALTER TABLE identity.workspace
  ADD COLUMN spend_ceiling_micro_usd bigint,
  ADD CONSTRAINT workspace_spend_ceiling_check CHECK (spend_ceiling_micro_usd IS NULL OR spend_ceiling_micro_usd >= 0);

-- Keep spend_ceiling_micro_usd writable only by the schema owner / provisioning
-- control plane, never by upmo_app (so settings:manage cannot raise or clear it).
REVOKE UPDATE ON identity.workspace FROM upmo_app;
GRANT UPDATE (
  name,
  logo_url,
  time_zone,
  locale,
  default_route,
  default_agent_id,
  allow_member_providers,
  allow_member_routes,
  allow_member_connectors,
  allow_member_agents,
  sso_required,
  updated_at
) ON identity.workspace TO upmo_app;

-- +goose Down

REVOKE UPDATE ON identity.workspace FROM upmo_app;
GRANT UPDATE ON identity.workspace TO upmo_app;

ALTER TABLE identity.workspace
  DROP CONSTRAINT IF EXISTS workspace_spend_ceiling_check,
  DROP COLUMN IF EXISTS spend_ceiling_micro_usd;

DROP INDEX IF EXISTS identity.teams_external_id;
ALTER TABLE identity.teams
  DROP COLUMN IF EXISTS external_id;
