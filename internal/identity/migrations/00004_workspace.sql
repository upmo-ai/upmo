-- +goose Up

CREATE TABLE identity.workspace (
  id bool PRIMARY KEY DEFAULT true CHECK (id),
  name text NOT NULL DEFAULT 'Workspace',
  logo_url text NOT NULL DEFAULT '',
  time_zone text NOT NULL DEFAULT 'UTC',
  locale text NOT NULL DEFAULT 'en-GB',
  default_route text NOT NULL DEFAULT '',
  default_agent_id uuid,
  allow_member_providers bool NOT NULL DEFAULT true,
  allow_member_routes bool NOT NULL DEFAULT true,
  allow_member_connectors bool NOT NULL DEFAULT true,
  allow_member_agents bool NOT NULL DEFAULT true,
  sso_required bool NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT workspace_name_check CHECK (btrim(name) <> '')
);

INSERT INTO identity.workspace (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

-- This policy helper is the one rule for whether a caller's private provider
-- account is available. Settings managers remain able to use theirs while the
-- member switch is off.
-- +goose StatementBegin
CREATE FUNCTION identity.member_providers_allowed()
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT COALESCE((SELECT allow_member_providers FROM identity.workspace WHERE id), true)
    OR EXISTS (
      SELECT 1
      FROM identity.users AS u
      JOIN identity.roles AS r ON r.id = u.role_id
      WHERE u.id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
        AND u.joined_at IS NOT NULL
        AND u.disabled_at IS NULL
        AND 'settings:manage' = ANY (identity.role_permissions(r.key, r.permissions))
    );
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.member_providers_allowed() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.member_providers_allowed() TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.may_manage_provider(owner_user_id uuid, visibility text)
RETURNS boolean
LANGUAGE sql
STABLE
SET search_path = pg_catalog
AS $$
  SELECT identity.may_manage(owner_user_id, visibility)
    AND (visibility <> 'private'
      OR identity.member_providers_allowed()
      OR 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')));
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.may_manage_provider(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.may_manage_provider(uuid, text) TO upmo_app;

ALTER TABLE identity.users
  ADD COLUMN time_zone text,
  ADD COLUMN locale text;

ALTER TABLE identity.sessions DROP CONSTRAINT sessions_sign_in_method_check;
ALTER TABLE identity.sessions ADD CONSTRAINT sessions_sign_in_method_check
  CHECK (sign_in_method IN ('passkey', 'link', 'password', 'sso'));

ALTER TABLE identity.sign_in_links DROP CONSTRAINT sign_in_links_purpose_check;
ALTER TABLE identity.sign_in_links ADD CONSTRAINT sign_in_links_purpose_check
  CHECK (purpose IN ('invite', 'recover', 'bootstrap', 'sign_in', 'step_up', 'sso', 'sso_stepup'));

-- +goose StatementBegin
CREATE FUNCTION identity.workspace_owner()
RETURNS boolean
LANGUAGE sql
STABLE
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT current_user = (
    SELECT pg_catalog.pg_get_userbyid(c.relowner)
    FROM pg_catalog.pg_class AS c
    WHERE c.oid = 'identity.workspace'::regclass
  );
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.workspace_owner() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.workspace_owner() TO upmo_app;

ALTER TABLE identity.workspace ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.workspace FORCE ROW LEVEL SECURITY;

CREATE POLICY workspace_owner ON identity.workspace
  FOR ALL
  USING (identity.workspace_owner())
  WITH CHECK (identity.workspace_owner());

CREATE POLICY workspace_select ON identity.workspace
  FOR SELECT
  USING (NULLIF(current_setting('upmo.user_id', true), '')::uuid IS NOT NULL);

CREATE POLICY workspace_update ON identity.workspace
  FOR UPDATE
  USING (
    NULLIF(current_setting('upmo.user_id', true), '')::uuid IS NOT NULL
    AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  )
  WITH CHECK (
    NULLIF(current_setting('upmo.user_id', true), '')::uuid IS NOT NULL
    AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

GRANT SELECT, UPDATE ON identity.workspace TO upmo_app;

-- Sign-in flows run before a session exists, so upmo_app reads sso_required
-- and the person's system role key through this definer lookup instead of
-- opening direct anonymous SELECT on identity.workspace.
-- +goose StatementBegin
CREATE FUNCTION identity.sso_sign_in_state(p_user_id uuid)
RETURNS TABLE (sso_required boolean, role_key text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
  SELECT w.sso_required, COALESCE(r.key, '')
  FROM identity.workspace AS w
  JOIN identity.users AS u ON u.id = p_user_id
  JOIN identity.roles AS r ON r.id = u.role_id
  WHERE w.id = true
    AND u.disabled_at IS NULL
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.sso_sign_in_state(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.sso_sign_in_state(uuid) TO upmo_app;

-- +goose Down

DROP FUNCTION IF EXISTS identity.sso_sign_in_state(uuid);
DROP FUNCTION IF EXISTS identity.may_manage_provider(uuid, text);
DROP FUNCTION IF EXISTS identity.member_providers_allowed();
DROP POLICY IF EXISTS workspace_update ON identity.workspace;
DROP POLICY IF EXISTS workspace_select ON identity.workspace;
DROP POLICY IF EXISTS workspace_owner ON identity.workspace;
DROP FUNCTION IF EXISTS identity.workspace_owner();
DROP TABLE IF EXISTS identity.workspace;

ALTER TABLE identity.users
  DROP COLUMN IF EXISTS locale,
  DROP COLUMN IF EXISTS time_zone;

DELETE FROM identity.sign_in_links WHERE purpose NOT IN ('invite', 'recover', 'bootstrap');
ALTER TABLE identity.sign_in_links DROP CONSTRAINT IF EXISTS sign_in_links_purpose_check;
ALTER TABLE identity.sign_in_links ADD CONSTRAINT sign_in_links_purpose_check
  CHECK (purpose IN ('invite', 'recover', 'bootstrap'));

DELETE FROM identity.sessions WHERE sign_in_method NOT IN ('passkey', 'link', 'password');
ALTER TABLE identity.sessions DROP CONSTRAINT IF EXISTS sessions_sign_in_method_check;
ALTER TABLE identity.sessions ADD CONSTRAINT sessions_sign_in_method_check
  CHECK (sign_in_method IN ('passkey', 'link', 'password'));
