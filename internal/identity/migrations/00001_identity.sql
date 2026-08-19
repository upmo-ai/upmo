-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION identity.keep_joined_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  -- Two owner changes already hold row locks, so a table lock at commit
  -- deadlocks them. This lock serialises the check instead.
  PERFORM pg_advisory_xact_lock(7480199);
  IF EXISTS (SELECT 1 FROM identity.users)
     AND NOT EXISTS (
       SELECT 1
       FROM identity.users AS person
       JOIN identity.roles AS role ON role.id = person.role_id
       WHERE role.key = 'owner'
         AND person.joined_at IS NOT NULL
         AND person.disabled_at IS NULL
     ) THEN
    RAISE EXCEPTION 'install has no joined owner';
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION identity.team_member_has_joined()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM identity.users AS person
    WHERE person.id = NEW.user_id
      AND person.joined_at IS NOT NULL
      AND person.disabled_at IS NULL
  ) THEN
    RAISE EXCEPTION 'team member has not joined';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION identity.keep_system_roles()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF OLD.key IS NOT NULL THEN
    RAISE EXCEPTION 'a system role cannot be changed';
  END IF;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TABLE identity.roles (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  key text,
  name text NOT NULL,
  permissions text[] NOT NULL DEFAULT '{}',
  CONSTRAINT roles_key_check CHECK (key IS NULL OR key IN ('owner', 'admin', 'member', 'viewer')),
  CONSTRAINT roles_permissions_check CHECK (
    key IS NOT DISTINCT FROM 'owner'
    OR NOT (permissions && ARRAY['owners:manage']::text[])
  )
);

CREATE UNIQUE INDEX roles_system_key ON identity.roles (key) WHERE key IS NOT NULL;

CREATE UNIQUE INDEX roles_name_ci ON identity.roles (lower(name));

CREATE TRIGGER roles_keep_system
BEFORE UPDATE OR DELETE ON identity.roles
FOR EACH ROW
EXECUTE FUNCTION identity.keep_system_roles();

INSERT INTO identity.roles (key, name)
VALUES
  ('owner', 'Owner'),
  ('admin', 'Admin'),
  ('member', 'Member'),
  ('viewer', 'Viewer');

CREATE TABLE identity.users (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  email text NOT NULL,
  name text NOT NULL,
  external_id text,
  role_id uuid NOT NULL REFERENCES identity.roles (id),
  invited_at timestamptz,
  joined_at timestamptz,
  disabled_at timestamptz,
  webauthn_handle bytea,
  password_hash text,
  passkey_offered_at timestamptz,
  CONSTRAINT users_name_check CHECK (name <> '')
);

CREATE UNIQUE INDEX users_email_ci ON identity.users (lower(email));

CREATE UNIQUE INDEX users_external_id ON identity.users (external_id) WHERE external_id IS NOT NULL;

CREATE UNIQUE INDEX users_webauthn_handle ON identity.users (webauthn_handle) WHERE webauthn_handle IS NOT NULL;

CREATE CONSTRAINT TRIGGER users_keep_joined_owner
AFTER INSERT OR DELETE OR UPDATE OF role_id, joined_at, disabled_at ON identity.users
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION identity.keep_joined_owner();

CREATE TABLE identity.teams (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  name text NOT NULL
);

CREATE UNIQUE INDEX teams_name_ci ON identity.teams (lower(name));

CREATE TABLE identity.team_members (
  team_id uuid NOT NULL REFERENCES identity.teams (id),
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  PRIMARY KEY (team_id, user_id)
);

CREATE TRIGGER team_members_must_have_joined
BEFORE INSERT OR UPDATE ON identity.team_members
FOR EACH ROW
EXECUTE FUNCTION identity.team_member_has_joined();

CREATE TABLE identity.principals (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  kind text NOT NULL,
  user_id uuid REFERENCES identity.users (id),
  agent_id uuid,
  run_id uuid,
  CONSTRAINT principals_kind_check CHECK (
    (kind = 'user' AND user_id IS NOT NULL AND agent_id IS NULL AND run_id IS NULL)
    OR (kind = 'agent' AND agent_id IS NOT NULL AND user_id IS NULL AND run_id IS NULL)
    OR (kind = 'run' AND run_id IS NOT NULL AND user_id IS NOT NULL AND agent_id IS NULL)
  )
);

CREATE TABLE identity.shares (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL,
  object_id uuid NOT NULL,
  user_id uuid REFERENCES identity.users (id),
  team_id uuid REFERENCES identity.teams (id),
  grantee_role_id uuid REFERENCES identity.roles (id),
  level text NOT NULL,
  granted_by uuid NOT NULL REFERENCES identity.users (id),
  CONSTRAINT shares_level_check CHECK (level IN ('editor', 'viewer')),
  CONSTRAINT shares_grantee_check CHECK (
    (user_id IS NOT NULL AND team_id IS NULL AND grantee_role_id IS NULL)
    OR (user_id IS NULL AND team_id IS NOT NULL AND grantee_role_id IS NULL)
    OR (user_id IS NULL AND team_id IS NULL AND grantee_role_id IS NOT NULL)
  )
);

CREATE UNIQUE INDEX shares_user_once ON identity.shares (object_type, object_id, user_id) WHERE user_id IS NOT NULL;

CREATE UNIQUE INDEX shares_team_once ON identity.shares (object_type, object_id, team_id) WHERE team_id IS NOT NULL;

CREATE UNIQUE INDEX shares_role_once ON identity.shares (object_type, object_id, grantee_role_id) WHERE grantee_role_id IS NOT NULL;

CREATE INDEX shares_object ON identity.shares (object_type, object_id);

CREATE TABLE identity.sessions (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  created_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  sign_in_method text NOT NULL DEFAULT 'passkey',
  signed_in_at timestamptz NOT NULL DEFAULT now(),
  user_agent text NOT NULL DEFAULT '',
  CONSTRAINT sessions_sign_in_method_check CHECK (sign_in_method IN ('passkey', 'link', 'password'))
);

-- +goose StatementBegin
CREATE FUNCTION identity.delete_sessions_on_disable()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.disabled_at IS NOT NULL AND OLD.disabled_at IS NULL THEN
    DELETE FROM identity.sessions WHERE user_id = NEW.id;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_delete_sessions
AFTER UPDATE OF disabled_at ON identity.users
FOR EACH ROW
EXECUTE FUNCTION identity.delete_sessions_on_disable();

CREATE TABLE identity.passkeys (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  credential_id bytea NOT NULL UNIQUE,
  public_key bytea NOT NULL,
  sign_count bigint NOT NULL,
  aaguid uuid NOT NULL,
  transports text[] NOT NULL,
  backup_eligible boolean NOT NULL,
  backup_state boolean NOT NULL,
  attestation text NOT NULL,
  name text NOT NULL,
  created_at timestamptz NOT NULL,
  last_used_at timestamptz
);

CREATE TABLE identity.webauthn_challenges (
  token_hash bytea PRIMARY KEY,
  session jsonb NOT NULL,
  purpose text NOT NULL,
  user_id uuid,
  expires_at timestamptz NOT NULL
);

CREATE TABLE identity.sign_in_links (
  id uuid NOT NULL DEFAULT uuidv7(),
  token_hash bytea PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  purpose text NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  CONSTRAINT sign_in_links_purpose_check CHECK (purpose IN ('invite', 'recover', 'bootstrap'))
);

CREATE UNIQUE INDEX sign_in_links_id ON identity.sign_in_links (id);

CREATE TABLE identity.throttle (
  key text PRIMARY KEY,
  tokens double precision NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE INDEX throttle_updated ON identity.throttle (updated_at);

CREATE TABLE identity.api_tokens (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  name text NOT NULL,
  token_hash bytea NOT NULL UNIQUE,
  scope text[] NOT NULL,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  last_used_at timestamptz,
  CONSTRAINT api_tokens_expiry_check CHECK (expires_at > created_at)
);

CREATE TABLE identity.connector_grants (
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  connector text NOT NULL,
  resource_scope text NOT NULL,
  resource_ids text[] NOT NULL DEFAULT '{}',
  changes text NOT NULL,
  PRIMARY KEY (user_id, connector),
  CONSTRAINT connector_grants_scope_check CHECK (resource_scope IN ('all', 'these')),
  CONSTRAINT connector_grants_changes_check CHECK (changes IN ('allow', 'ask', 'block')),
  CONSTRAINT connector_grants_ids_check CHECK (
    (resource_scope = 'all' AND resource_ids = '{}')
    OR (resource_scope = 'these' AND resource_ids <> '{}')
  )
);

-- upmo_app is created NOBYPASSRLS (the default). Do not ALTER ROLE here:
-- the role is cluster-wide and parallel migrators would race on pg_authid.

-- +goose StatementBegin
DO $$
BEGIN
  CREATE ROLE upmo_app NOLOGIN;
EXCEPTION
  WHEN duplicate_object THEN NULL;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA identity TO upmo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA identity TO upmo_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA identity TO upmo_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA identity TO upmo_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA identity GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO upmo_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA identity GRANT USAGE, SELECT ON SEQUENCES TO upmo_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA identity GRANT EXECUTE ON FUNCTIONS TO upmo_app;
REVOKE ALL ON identity.goose_db_version FROM upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.may(
  action text,
  object_type text,
  owner_user_id uuid,
  visibility text,
  visibility_team_id uuid,
  id uuid
) RETURNS boolean
LANGUAGE sql
STABLE
SET search_path = pg_catalog
AS $$
  SELECT
    CASE
      WHEN action = 'create' THEN
        owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
        AND (object_type || ':create') = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
      ELSE
        (object_type || ':' || action) = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
        AND (
          owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
          OR id = ANY (COALESCE(NULLIF(
            CASE
              WHEN action IN ('edit', 'delete') THEN current_setting('upmo.shared_edit', true)
              ELSE current_setting('upmo.shared_view', true)
            END, ''), '{}')::uuid[])
          OR (
            action = 'view'
            AND (
              visibility = 'everyone'
              OR (
                visibility = 'team'
                AND visibility_team_id = ANY (COALESCE(NULLIF(current_setting('upmo.teams', true), ''), '{}')::uuid[])
              )
            )
          )
        )
    END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.may(text, text, uuid, text, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.may(text, text, uuid, text, uuid, uuid) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.protect(target regclass)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
  nsp name;
  cols text;
BEGIN
  SELECT n.nspname INTO nsp
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE c.oid = target AND c.relkind = 'r';
  IF nsp IS NULL THEN
    RAISE EXCEPTION 'identity.protect: % is not a table', target;
  END IF;
  IF nsp = 'identity' THEN
    RAISE EXCEPTION 'identity.protect: identity tables stay outside row security';
  END IF;

  EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', target);
  EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', target);

  EXECUTE format('DROP POLICY IF EXISTS may_view ON %s', target);
  EXECUTE format('DROP POLICY IF EXISTS may_edit ON %s', target);
  EXECUTE format('DROP POLICY IF EXISTS may_delete ON %s', target);
  EXECUTE format('DROP POLICY IF EXISTS may_create ON %s', target);

  EXECUTE format(
    $p$CREATE POLICY may_view ON %s FOR SELECT
      USING (identity.may('view', object_type, owner_user_id, visibility, visibility_team_id, id))$p$,
    target
  );
  EXECUTE format(
    $p$CREATE POLICY may_edit ON %s FOR UPDATE
      USING (identity.may('edit', object_type, owner_user_id, visibility, visibility_team_id, id))
      WITH CHECK (identity.may('edit', object_type, owner_user_id, visibility, visibility_team_id, id))$p$,
    target
  );
  EXECUTE format(
    $p$CREATE POLICY may_delete ON %s FOR DELETE
      USING (identity.may('delete', object_type, owner_user_id, visibility, visibility_team_id, id))$p$,
    target
  );
  EXECUTE format(
    $p$CREATE POLICY may_create ON %s FOR INSERT
      WITH CHECK (identity.may('create', object_type, owner_user_id, visibility, visibility_team_id, id))$p$,
    target
  );

  SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
  INTO cols
  FROM pg_attribute a
  WHERE a.attrelid = target
    AND a.attnum > 0
    AND NOT a.attisdropped
    AND a.attname <> 'owner_user_id';
  IF cols IS NULL THEN
    RAISE EXCEPTION 'identity.protect: % has no updatable columns', target;
  END IF;

  EXECUTE format('GRANT USAGE ON SCHEMA %I TO upmo_app', nsp);
  EXECUTE format('GRANT SELECT, INSERT, DELETE ON %s TO upmo_app', target);
  EXECUTE format('GRANT UPDATE (%s) ON %s TO upmo_app', cols, target);
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.protect(regclass) FROM PUBLIC;
REVOKE ALL ON FUNCTION identity.protect(regclass) FROM upmo_app;
REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA identity FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = CURRENT_USER AND rolsuper) THEN
    EXECUTE format('GRANT upmo_app TO %I WITH INHERIT FALSE, SET TRUE', CURRENT_USER);
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS identity.protect(regclass);
DROP FUNCTION IF EXISTS identity.may(text, text, uuid, text, uuid, uuid);
REVOKE ALL ON ALL TABLES IN SCHEMA identity FROM upmo_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA identity FROM upmo_app;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA identity FROM upmo_app;
REVOKE USAGE ON SCHEMA identity FROM upmo_app;
DROP TABLE IF EXISTS identity.connector_grants;
DROP TABLE IF EXISTS identity.api_tokens;
DROP TABLE IF EXISTS identity.throttle;
DROP TABLE IF EXISTS identity.sign_in_links;
DROP TABLE IF EXISTS identity.webauthn_challenges;
DROP TABLE IF EXISTS identity.passkeys;
DROP TRIGGER IF EXISTS users_delete_sessions ON identity.users;
DROP FUNCTION IF EXISTS identity.delete_sessions_on_disable();
DROP TABLE IF EXISTS identity.sessions;
DROP TABLE IF EXISTS identity.shares;
DROP TABLE IF EXISTS identity.principals;
DROP TABLE IF EXISTS identity.team_members;
DROP TABLE IF EXISTS identity.teams;
DROP TABLE IF EXISTS identity.users;
DROP TABLE IF EXISTS identity.roles;
DROP FUNCTION IF EXISTS identity.keep_system_roles();
DROP FUNCTION IF EXISTS identity.team_member_has_joined();
DROP FUNCTION IF EXISTS identity.keep_joined_owner();
