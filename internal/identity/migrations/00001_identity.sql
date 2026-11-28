-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION identity.keep_joined_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  -- Two owner changes already hold row locks, so a table lock at commit
  -- deadlocks them. This lock serializes the check instead.
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
  email_hash bytea NOT NULL,
  email_hash_key_id text NOT NULL,
  name text NOT NULL,
  external_id text,
  role_id uuid NOT NULL REFERENCES identity.roles (id),
  invited_at timestamptz,
  joined_at timestamptz,
  disabled_at timestamptz,
  webauthn_handle bytea,
  password_hash text,
  passkey_offered_at timestamptz,
  time_zone text,
  locale text,
  avatar_key text,
  CONSTRAINT users_name_check CHECK (name <> ''),
  CONSTRAINT users_email_hash_len CHECK (octet_length(email_hash) = 32)
);

CREATE UNIQUE INDEX users_email_hash ON identity.users (email_hash);

CREATE UNIQUE INDEX users_external_id ON identity.users (external_id) WHERE external_id IS NOT NULL;

CREATE UNIQUE INDEX users_webauthn_handle ON identity.users (webauthn_handle) WHERE webauthn_handle IS NOT NULL;

CREATE CONSTRAINT TRIGGER users_keep_joined_owner
AFTER INSERT OR DELETE OR UPDATE OF role_id, joined_at, disabled_at ON identity.users
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION identity.keep_joined_owner();

CREATE TABLE identity.teams (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  name text NOT NULL,
  external_id text
);

CREATE UNIQUE INDEX teams_name_ci ON identity.teams (lower(name));
CREATE UNIQUE INDEX teams_external_id ON identity.teams (external_id) WHERE external_id IS NOT NULL;

CREATE TABLE identity.team_members (
  team_id uuid NOT NULL REFERENCES identity.teams (id),
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  PRIMARY KEY (team_id, user_id)
);

CREATE TRIGGER team_members_must_have_joined
BEFORE INSERT OR UPDATE ON identity.team_members
FOR EACH ROW
EXECUTE FUNCTION identity.team_member_has_joined();

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
  CONSTRAINT sessions_sign_in_method_check CHECK (sign_in_method IN ('passkey', 'link', 'password', 'sso'))
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
  CONSTRAINT sign_in_links_purpose_check CHECK (purpose IN ('invite', 'recover', 'bootstrap', 'sign_in', 'step_up', 'sso', 'sso_stepup'))
);

CREATE UNIQUE INDEX sign_in_links_id ON identity.sign_in_links (id);

CREATE TABLE identity.throttle (
  key text PRIMARY KEY,
  tokens double precision NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE INDEX throttle_updated ON identity.throttle (updated_at);

-- Password checks per address, counted by the worker as the owner: subject
-- is the current email index MAC of the address, so an unknown address
-- counts the same as a real one. A worker restart keeps the count. No
-- serving role may read or write it.
CREATE TABLE identity.password_verify_window (
  subject bytea PRIMARY KEY CHECK (octet_length(subject) = 32),
  window_start timestamptz NOT NULL,
  attempts integer NOT NULL CHECK (attempts BETWEEN 1 AND 6)
);

CREATE INDEX password_verify_window_start ON identity.password_verify_window (window_start);

ALTER TABLE identity.password_verify_window ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.password_verify_window FORCE ROW LEVEL SECURITY;

CREATE POLICY password_verify_window_owner ON identity.password_verify_window
  FOR ALL
  USING (current_user = (
    SELECT pg_catalog.pg_get_userbyid(c.relowner)
    FROM pg_catalog.pg_class AS c
    WHERE c.oid = 'identity.password_verify_window'::regclass))
  WITH CHECK (current_user = (
    SELECT pg_catalog.pg_get_userbyid(c.relowner)
    FROM pg_catalog.pg_class AS c
    WHERE c.oid = 'identity.password_verify_window'::regclass));

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
  spend_ceiling_micro_usd bigint,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT workspace_name_check CHECK (btrim(name) <> ''),
  CONSTRAINT workspace_spend_ceiling_check CHECK (spend_ceiling_micro_usd IS NULL OR spend_ceiling_micro_usd >= 0)
);

INSERT INTO identity.workspace (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

-- One row per accepted provision bearer: the key that verified it and the
-- nonce it carried. The primary key makes every nonce single-use per key.
CREATE TABLE identity.provision_nonces (
  key_id  text        NOT NULL,
  nonce   text        NOT NULL,
  seen_at timestamptz NOT NULL,
  PRIMARY KEY (key_id, nonce)
);

-- upmo_app is created NOBYPASSRLS (the default). Do not ALTER ROLE here:
-- the role is cluster-wide and parallel migrators would race on pg_authid.

-- +goose StatementBegin
DO $$
BEGIN
  CREATE ROLE upmo_app NOLOGIN;
EXCEPTION
  -- Concurrent creators collide on pg_authid_rolname_index before the
  -- duplicate_object check runs, which surfaces as unique_violation.
  WHEN duplicate_object OR unique_violation THEN NULL;
END
$$;
-- +goose StatementEnd

-- The other serving roles follow the same rule: NOLOGIN until the worker
-- sets a password from their URL, NOSUPERUSER, NOBYPASSRLS, and owner of no
-- table. upmo_gateway dispatches model calls, upmo_sandbox reads one bound
-- credential through gateway.credential_for_sandbox, and upmo_runner writes
-- the task runner columns brain may not write as upmo_app.

-- +goose StatementBegin
DO $$
BEGIN
  BEGIN
    CREATE ROLE upmo_gateway NOLOGIN NOSUPERUSER NOBYPASSRLS;
  EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
  END;
  BEGIN
    CREATE ROLE upmo_sandbox NOLOGIN NOSUPERUSER NOBYPASSRLS;
  EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
  END;
  BEGIN
    CREATE ROLE upmo_runner NOLOGIN NOSUPERUSER NOBYPASSRLS;
  EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
  END;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA identity TO upmo_app;
GRANT USAGE ON SCHEMA identity TO upmo_gateway, upmo_runner;
GRANT SELECT ON identity.roles TO upmo_app;
-- Every column but password_hash and email: brain never reads a stored
-- password or a stored address. The worker verifies a password and opens an
-- address for a proof, and brain asks has_password or has_password_of.
GRANT SELECT (id, email_hash, email_hash_key_id, name, external_id, role_id, invited_at, joined_at,
  disabled_at, webauthn_handle, passkey_offered_at, time_zone, locale, avatar_key) ON identity.users TO upmo_app;
GRANT UPDATE (password_hash, webauthn_handle, joined_at, passkey_offered_at) ON identity.users TO upmo_app;
GRANT SELECT ON identity.teams TO upmo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON
  identity.passkeys,
  identity.throttle,
  identity.connector_grants
TO upmo_app;
-- upmo_app only reads shares and team members. It writes them only through
-- a definer that checks the actor's session.
GRANT SELECT ON identity.team_members, identity.shares TO upmo_app;
GRANT SELECT (id, user_id, created_at, last_seen_at, expires_at, sign_in_method, signed_in_at, user_agent) ON identity.sessions TO upmo_app;
GRANT INSERT, DELETE ON identity.sessions TO upmo_app;
GRANT UPDATE (last_seen_at, sign_in_method, signed_in_at) ON identity.sessions TO upmo_app;
GRANT SELECT (id, user_id, name, scope, created_at, expires_at, last_used_at) ON identity.api_tokens TO upmo_app;
GRANT DELETE ON identity.api_tokens TO upmo_app;
GRANT UPDATE (last_used_at) ON identity.api_tokens TO upmo_app;
GRANT SELECT (id, user_id, purpose, expires_at, used_at) ON identity.sign_in_links TO upmo_app;
GRANT INSERT, UPDATE, DELETE ON identity.sign_in_links TO upmo_app;
GRANT SELECT (purpose, user_id, expires_at) ON identity.webauthn_challenges TO upmo_app;
GRANT INSERT, UPDATE, DELETE ON identity.webauthn_challenges TO upmo_app;
GRANT SELECT ON identity.workspace TO upmo_app;
-- Keep spend_ceiling_micro_usd writable only by the schema owner / provisioning
-- control plane, never by upmo_app (so settings:manage cannot raise or clear it).
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
-- identity.provision_nonces stays owner-only with zero grants to upmo_app.
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA identity TO upmo_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA identity TO upmo_app;
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

REVOKE ALL ON FUNCTION identity.protect(regclass) FROM PUBLIC, upmo_app;

-- Hash lookups for upmo_app, which cannot read the hash columns directly.
-- Each one is a definer function over the secret columns only, so a leaked
-- app credential still cannot bulk-read them.

-- +goose StatementBegin
CREATE FUNCTION identity.session_find(hash bytea)
RETURNS TABLE (
  id uuid,
  user_id uuid,
  created_at timestamptz,
  last_seen_at timestamptz,
  expires_at timestamptz,
  sign_in_method text,
  signed_in_at timestamptz,
  role_id uuid,
  avatar_key text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
  SELECT session.id, session.user_id, session.created_at, session.last_seen_at,
         session.expires_at, session.sign_in_method, session.signed_in_at,
         person.role_id, COALESCE(person.avatar_key, '')
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  WHERE session.token_hash = hash
    AND person.disabled_at IS NULL
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.session_find(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.session_find(bytea) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.token_find(hash bytea)
RETURNS TABLE (
  id uuid,
  name text,
  scope text[],
  created_at timestamptz,
  expires_at timestamptz,
  last_used_at timestamptz,
  person_id uuid,
  disabled boolean,
  joined boolean,
  role_id uuid,
  role_key text,
  permissions text[]
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
  SELECT token.id, token.name, token.scope, token.created_at, token.expires_at,
         token.last_used_at, person.id, person.disabled_at IS NOT NULL,
         person.joined_at IS NOT NULL, person.role_id, COALESCE(role.key, ''),
         role.permissions
  FROM identity.api_tokens AS token
  JOIN identity.users AS person ON person.id = token.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE token.token_hash = hash
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.token_find(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.token_find(bytea) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.link_find(hash bytea, at timestamptz)
RETURNS TABLE (user_id uuid, purpose text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
  SELECT link.user_id, link.purpose
  FROM identity.sign_in_links AS link
  JOIN identity.users AS person ON person.id = link.user_id
  WHERE link.token_hash = hash
    AND link.used_at IS NULL
    AND link.expires_at > at
    AND person.disabled_at IS NULL
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.link_find(bytea, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.link_find(bytea, timestamptz) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.link_spend(hash bytea, at timestamptz)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
  UPDATE identity.sign_in_links AS link
  SET used_at = at
  WHERE link.token_hash = hash
    AND link.used_at IS NULL
    AND link.expires_at > at;
  RETURN FOUND;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.link_spend(bytea, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.link_spend(bytea, timestamptz) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.challenge_take(hash bytea, at timestamptz)
RETURNS TABLE (session jsonb, purpose text, user_id uuid)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
  DELETE FROM identity.webauthn_challenges AS challenge
  WHERE challenge.token_hash = hash
    AND challenge.expires_at > at
  RETURNING challenge.session, challenge.purpose, challenge.user_id
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.challenge_take(bytea, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.challenge_take(bytea, timestamptz) TO upmo_app;

-- Shared access rules for config objects (providers, connectors, routes).
-- +goose StatementBegin
CREATE FUNCTION identity.can_use(
  me uuid, my_teams uuid[], owner_user_id uuid, visibility text, team_ids uuid[]
) RETURNS boolean
LANGUAGE sql IMMUTABLE
SET search_path = pg_catalog
AS $$
  SELECT me IS NOT NULL AND COALESCE(
    (visibility = 'private' AND owner_user_id = me)
    OR visibility = 'everyone'
    OR (visibility = 'team' AND team_ids && my_teams),
    false);
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.can_use(uuid, uuid[], uuid, text, uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.can_use(uuid, uuid[], uuid, text, uuid[]) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.may_use(owner_user_id uuid, visibility text, team_ids uuid[])
RETURNS boolean LANGUAGE sql STABLE SET search_path = pg_catalog
AS $$
  SELECT identity.can_use(
    NULLIF(current_setting('upmo.user_id', true), '')::uuid,
    COALESCE(NULLIF(current_setting('upmo.teams', true), ''), '{}')::uuid[],
    owner_user_id, visibility, team_ids);
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.may_use(uuid, text, uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.may_use(uuid, text, uuid[]) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.may_manage(owner_user_id uuid, visibility text)
RETURNS boolean LANGUAGE sql STABLE SET search_path = pg_catalog
AS $$
  SELECT COALESCE(
    (visibility = 'private'
      AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
    OR (visibility IN ('team', 'everyone')
      AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))),
    false);
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.may_manage(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.may_manage(uuid, text) TO upmo_app;

-- +goose StatementBegin
-- The team ids of one person, and nothing else about anyone. Gateway routes
-- a call through it instead of reading identity.team_members.
CREATE FUNCTION identity.teams_of(user_id uuid) RETURNS uuid[]
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
  SELECT COALESCE(array_agg(m.team_id ORDER BY m.team_id), '{}'::uuid[])
  FROM identity.team_members AS m WHERE m.user_id = teams_of.user_id;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.teams_of(uuid) FROM PUBLIC, upmo_app;
GRANT EXECUTE ON FUNCTION identity.teams_of(uuid) TO upmo_gateway;

-- Pure catalog expansion matching domain.SystemPermissions and domain.known.
-- +goose StatementBegin
CREATE FUNCTION identity.role_permissions(role_key text, custom_permissions text[])
RETURNS text[]
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog
AS $$
  SELECT CASE
    WHEN role_key = 'owner' THEN ARRAY[
      'task:create', 'task:view', 'task:edit', 'task:share', 'task:delete',
      'chat:create', 'chat:view', 'chat:edit', 'chat:share', 'chat:delete',
      'folder:create', 'folder:view', 'folder:edit', 'folder:share', 'folder:delete',
      'automation:create', 'automation:view', 'automation:edit', 'automation:share', 'automation:delete',
      'agent:create', 'agent:view', 'agent:edit', 'agent:share', 'agent:delete',
      'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share', 'artifact:delete',
      'members:manage', 'teams:manage', 'roles:manage', 'settings:manage',
      'connectors:manage', 'owners:manage', 'audit:view', 'usage:view'
    ]::text[]
    WHEN role_key = 'admin' THEN ARRAY[
      'task:create', 'task:view', 'task:edit', 'task:share', 'task:delete',
      'chat:create', 'chat:view', 'chat:edit', 'chat:share', 'chat:delete',
      'folder:create', 'folder:view', 'folder:edit', 'folder:share', 'folder:delete',
      'automation:create', 'automation:view', 'automation:edit', 'automation:share', 'automation:delete',
      'agent:create', 'agent:view', 'agent:edit', 'agent:share', 'agent:delete',
      'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share', 'artifact:delete',
      'members:manage', 'teams:manage', 'roles:manage', 'settings:manage',
      'connectors:manage', 'audit:view', 'usage:view'
    ]::text[]
    WHEN role_key = 'member' THEN ARRAY[
      'task:create', 'task:view', 'task:edit', 'task:share', 'task:delete',
      'chat:create', 'chat:view', 'chat:edit', 'chat:share', 'chat:delete',
      'folder:create', 'folder:view', 'folder:edit', 'folder:share', 'folder:delete',
      'automation:create', 'automation:view', 'automation:edit', 'automation:share', 'automation:delete',
      'agent:create', 'agent:view', 'agent:edit', 'agent:share', 'agent:delete',
      'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share', 'artifact:delete'
    ]::text[]
    WHEN role_key = 'viewer' THEN ARRAY[
      'task:view', 'chat:view', 'folder:view', 'automation:view', 'agent:view', 'artifact:view'
    ]::text[]
    WHEN role_key IS NULL OR role_key = '' THEN COALESCE(
      (
        SELECT pg_catalog.array_agg(p)
        FROM pg_catalog.unnest(COALESCE(custom_permissions, '{}'::text[])) AS p
        WHERE p = ANY (ARRAY[
          'task:create', 'task:view', 'task:edit', 'task:share', 'task:delete',
          'chat:create', 'chat:view', 'chat:edit', 'chat:share', 'chat:delete',
          'folder:create', 'folder:view', 'folder:edit', 'folder:share', 'folder:delete',
          'automation:create', 'automation:view', 'automation:edit', 'automation:share', 'automation:delete',
          'agent:create', 'agent:view', 'agent:edit', 'agent:share', 'agent:delete',
          'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share', 'artifact:delete',
          'members:manage', 'teams:manage', 'roles:manage', 'settings:manage',
          'connectors:manage', 'audit:view', 'usage:view'
        ]::text[])
      ),
      '{}'::text[]
    )
    ELSE '{}'::text[]
  END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.role_permissions(text, text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.role_permissions(text, text[]) TO upmo_app;

-- The runner may revalidate one user's access without reading identity tables.
-- +goose StatementBegin
CREATE FUNCTION identity.runner_access(p_user_id uuid)
RETURNS TABLE (active boolean, allow text[], teams uuid[])
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT
    (u.joined_at IS NOT NULL AND u.disabled_at IS NULL) AS active,
    CASE
      WHEN u.joined_at IS NOT NULL AND u.disabled_at IS NULL
        THEN identity.role_permissions(r.key, r.permissions)
      ELSE '{}'::text[]
    END AS allow,
    CASE
      WHEN u.joined_at IS NOT NULL AND u.disabled_at IS NULL
        THEN COALESCE(
          (SELECT pg_catalog.array_agg(m.team_id ORDER BY m.team_id)
           FROM identity.team_members AS m
           WHERE m.user_id = u.id),
          '{}'::uuid[]
        )
      ELSE '{}'::uuid[]
    END AS teams
  FROM identity.users AS u
  JOIN identity.roles AS r ON r.id = u.role_id
  WHERE u.id = p_user_id;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.runner_access(uuid) FROM PUBLIC, upmo_app;
GRANT EXECUTE ON FUNCTION identity.runner_access(uuid) TO upmo_runner;

-- +goose StatementBegin
CREATE FUNCTION identity.member_set_role(
  actor_token_hash bytea,
  target_user_id uuid,
  target_role_id uuid
)
RETURNS TABLE (old_role_id uuid, new_role_id uuid)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_perms text[];
  v_old_role_id uuid;
  v_current_role_key text;
  v_next_role_key text;
  v_next_perms text[];
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF v_actor_id = target_user_id THEN
    RAISE EXCEPTION 'identity: you cannot do this to yourself' USING ERRCODE = '42501';
  END IF;

  SELECT person.role_id, COALESCE(role.key, '')
  INTO v_old_role_id, v_current_role_key
  FROM identity.users AS person
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE person.id = target_user_id
    AND person.disabled_at IS NULL
  FOR NO KEY UPDATE OF person;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such member' USING ERRCODE = 'P0002';
  END IF;

  SELECT COALESCE(role.key, ''), identity.role_permissions(role.key, role.permissions)
  INTO v_next_role_key, v_next_perms
  FROM identity.roles AS role
  WHERE role.id = target_role_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such role' USING ERRCODE = 'P0002';
  END IF;

  IF NOT ('members:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only owners and admins manage members' USING ERRCODE = '42501';
  END IF;

  IF (v_current_role_key = 'owner' OR v_next_role_key = 'owner')
     AND NOT ('owners:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only an owner can change the owner role' USING ERRCODE = '42501';
  END IF;

  IF v_next_role_key <> 'owner' AND NOT (v_next_perms <@ v_actor_perms) THEN
    RAISE EXCEPTION 'identity: you cannot grant a permission you do not hold' USING ERRCODE = '42501';
  END IF;

  UPDATE identity.users
  SET role_id = target_role_id
  WHERE id = target_user_id;

  old_role_id := v_old_role_id;
  new_role_id := target_role_id;
  RETURN NEXT;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.member_set_role(bytea, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.member_set_role(bytea, uuid, uuid) TO upmo_app;

-- names_of returns the names of the enabled people in person_ids and of the
-- teams in team_ids, for the actor behind actor_token_hash. Any joined actor
-- already lists every enabled member and every team, so no per-id check.
-- +goose StatementBegin
CREATE FUNCTION identity.names_of(
  actor_token_hash bytea,
  person_ids uuid[],
  team_ids uuid[]
)
RETURNS TABLE (kind text, id uuid, name text)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id
  INTO v_actor_id
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  RETURN QUERY
    SELECT 'person'::text, person.id, person.name
    FROM identity.users AS person
    WHERE person.id = ANY (person_ids)
      AND person.disabled_at IS NULL;

  RETURN QUERY
    SELECT 'team'::text, team.id, team.name
    FROM identity.teams AS team
    WHERE team.id = ANY (team_ids);
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.names_of(bytea, uuid[], uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.names_of(bytea, uuid[], uuid[]) TO upmo_app;

-- role_directory lists every role with the count of its enabled people, for
-- the actor behind actor_token_hash.
-- +goose StatementBegin
CREATE FUNCTION identity.role_directory(
  actor_token_hash bytea
)
RETURNS TABLE (id uuid, name text, key text, permissions text[], members bigint)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id
  INTO v_actor_id
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  RETURN QUERY
    SELECT role.id, role.name, COALESCE(role.key, ''), role.permissions,
           (SELECT count(*) FROM identity.users AS person
            WHERE person.role_id = role.id AND person.disabled_at IS NULL)
    FROM identity.roles AS role
    ORDER BY role.name;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.role_directory(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.role_directory(bytea) TO upmo_app;

-- object_grants lists the person and team grants of one task or chat with
-- the names of their subjects, in the order they were made, for the actor
-- behind actor_token_hash. Role grants are not listed.
-- +goose StatementBegin
CREATE FUNCTION identity.object_grants(
  actor_token_hash bytea,
  object_type text,
  object_id uuid
)
RETURNS TABLE (subject_type text, subject_id uuid, name text, level text)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id
  INTO v_actor_id
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF object_grants.object_type IS NULL OR object_grants.object_type NOT IN ('task', 'chat') THEN
    RAISE EXCEPTION 'identity: unknown object type' USING ERRCODE = '22023';
  END IF;

  RETURN QUERY
    SELECT CASE WHEN share.user_id IS NOT NULL THEN 'person' ELSE 'team' END::text,
           COALESCE(share.user_id, share.team_id),
           COALESCE(person.name, team.name, ''),
           share.level
    FROM identity.shares AS share
    LEFT JOIN identity.users AS person ON person.id = share.user_id
    LEFT JOIN identity.teams AS team ON team.id = share.team_id
    WHERE share.object_type = object_grants.object_type
      AND share.object_id = object_grants.object_id
      AND share.grantee_role_id IS NULL
    ORDER BY share.id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.object_grants(bytea, text, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.object_grants(bytea, text, uuid) TO upmo_app;

-- replace_object_grants replaces the person and team grants of one task or
-- chat in the caller's transaction: grants not listed are deleted, new ones
-- are inserted with the actor as grantor, and a changed level is updated.
-- Role grants are untouched. grants is a JSON array of
-- {"subject_type": "person"|"team", "subject_id": "<uuid>", "level": "<level>"}.
-- +goose StatementBegin
CREATE FUNCTION identity.replace_object_grants(
  actor_token_hash bytea,
  object_type text,
  object_id uuid,
  grants jsonb
)
RETURNS void
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_grant jsonb;
  v_type text;
  v_id text;
  v_keys text[] := '{}';
  v_stored record;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id
  INTO v_actor_id
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF replace_object_grants.object_type IS NULL OR replace_object_grants.object_type NOT IN ('task', 'chat') THEN
    RAISE EXCEPTION 'identity: unknown object type' USING ERRCODE = '22023';
  END IF;

  IF grants IS NULL OR pg_catalog.jsonb_typeof(grants) <> 'array' THEN
    RAISE EXCEPTION 'identity: grants must be an array' USING ERRCODE = '22023';
  END IF;

  -- Check the whole list before any write, so a refusal writes nothing.
  FOR v_grant IN SELECT value FROM pg_catalog.jsonb_array_elements(grants) LOOP
    v_type := v_grant ->> 'subject_type';
    v_id := v_grant ->> 'subject_id';
    IF v_type IS NULL OR v_type NOT IN ('person', 'team') THEN
      RAISE EXCEPTION 'identity: unknown subject type' USING ERRCODE = '22023';
    END IF;
    IF v_id IS NULL OR v_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
      RAISE EXCEPTION 'identity: invalid subject id' USING ERRCODE = '22023';
    END IF;
    IF (v_type || ':' || v_id::uuid::text) = ANY (v_keys) THEN
      RAISE EXCEPTION 'identity: duplicate grant subject' USING ERRCODE = '22023';
    END IF;
    v_keys := v_keys || (v_type || ':' || v_id::uuid::text);
    IF v_type = 'person' AND NOT EXISTS (
      SELECT 1 FROM identity.users AS person
      WHERE person.id = v_id::uuid AND person.disabled_at IS NULL
    ) THEN
      RAISE EXCEPTION 'identity: no such grantee' USING ERRCODE = 'P0002';
    END IF;
    IF v_type = 'team' AND NOT EXISTS (
      SELECT 1 FROM identity.teams AS team WHERE team.id = v_id::uuid
    ) THEN
      RAISE EXCEPTION 'identity: no such grantee' USING ERRCODE = 'P0002';
    END IF;
  END LOOP;

  -- An object with no grants has no rows to lock, so two replacements would
  -- both insert and the loser would fail the unique index: one advisory lock
  -- per object makes the second wait and then converge.
  PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended(replace_object_grants.object_id::text, 0::bigint));

  FOR v_stored IN
    SELECT share.id,
           CASE WHEN share.user_id IS NOT NULL THEN 'person:' || share.user_id::text
                ELSE 'team:' || share.team_id::text END AS subject_key,
           share.level
    FROM identity.shares AS share
    WHERE share.object_type = replace_object_grants.object_type
      AND share.object_id = replace_object_grants.object_id
      AND share.grantee_role_id IS NULL
    ORDER BY share.id
    FOR UPDATE
  LOOP
    IF NOT (v_stored.subject_key = ANY (v_keys)) THEN
      DELETE FROM identity.shares AS share WHERE share.id = v_stored.id;
    END IF;
  END LOOP;

  FOR v_grant IN SELECT value FROM pg_catalog.jsonb_array_elements(grants) LOOP
    v_type := v_grant ->> 'subject_type';
    v_id := (v_grant ->> 'subject_id')::uuid::text;
    IF v_type = 'person' THEN
      UPDATE identity.shares AS share SET level = v_grant ->> 'level'
      WHERE share.object_type = replace_object_grants.object_type
        AND share.object_id = replace_object_grants.object_id
        AND share.user_id = v_id::uuid
        AND share.level IS DISTINCT FROM v_grant ->> 'level';
      IF NOT EXISTS (
        SELECT 1 FROM identity.shares AS share
        WHERE share.object_type = replace_object_grants.object_type
          AND share.object_id = replace_object_grants.object_id
          AND share.user_id = v_id::uuid
      ) THEN
        INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
        VALUES (replace_object_grants.object_type, replace_object_grants.object_id, v_id::uuid, v_grant ->> 'level', v_actor_id);
      END IF;
    ELSE
      UPDATE identity.shares AS share SET level = v_grant ->> 'level'
      WHERE share.object_type = replace_object_grants.object_type
        AND share.object_id = replace_object_grants.object_id
        AND share.team_id = v_id::uuid
        AND share.level IS DISTINCT FROM v_grant ->> 'level';
      IF NOT EXISTS (
        SELECT 1 FROM identity.shares AS share
        WHERE share.object_type = replace_object_grants.object_type
          AND share.object_id = replace_object_grants.object_id
          AND share.team_id = v_id::uuid
      ) THEN
        INSERT INTO identity.shares (object_type, object_id, team_id, level, granted_by)
        VALUES (replace_object_grants.object_type, replace_object_grants.object_id, v_id::uuid, v_grant ->> 'level', v_actor_id);
      END IF;
    END IF;
  END LOOP;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.replace_object_grants(bytea, text, uuid, jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.replace_object_grants(bytea, text, uuid, jsonb) TO upmo_app;

-- add_team_member puts a joined, enabled person on a team for an actor who
-- may manage teams. It answers true when the person was added and false when
-- they were on the team already. The caller records the audit event.
-- +goose StatementBegin
CREATE FUNCTION identity.add_team_member(
  actor_token_hash bytea,
  team_id uuid,
  user_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_perms text[];
  v_joined boolean;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF NOT ('teams:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only team managers manage teams' USING ERRCODE = '42501';
  END IF;

  SELECT person.joined_at IS NOT NULL
  INTO v_joined
  FROM identity.users AS person
  WHERE person.id = add_team_member.user_id
    AND person.disabled_at IS NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such member' USING ERRCODE = 'P0002';
  END IF;

  IF NOT v_joined THEN
    RAISE EXCEPTION 'identity: not joined' USING ERRCODE = 'P0002';
  END IF;

  PERFORM 1 FROM identity.teams AS team WHERE team.id = add_team_member.team_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such team' USING ERRCODE = 'P0002';
  END IF;

  INSERT INTO identity.team_members AS membership (team_id, user_id)
  VALUES (add_team_member.team_id, add_team_member.user_id)
  ON CONFLICT DO NOTHING;
  RETURN FOUND;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.add_team_member(bytea, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.add_team_member(bytea, uuid, uuid) TO upmo_app;

-- remove_team_member takes a person off a team for an actor who may manage
-- teams. It answers true when a membership was deleted and false when the
-- person was not on the team. The caller records the audit event.
-- +goose StatementBegin
CREATE FUNCTION identity.remove_team_member(
  actor_token_hash bytea,
  team_id uuid,
  user_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_perms text[];
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT identity.role_permissions(role.key, role.permissions)
  INTO v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF NOT ('teams:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only team managers manage teams' USING ERRCODE = '42501';
  END IF;

  PERFORM 1 FROM identity.teams AS team WHERE team.id = remove_team_member.team_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such team' USING ERRCODE = 'P0002';
  END IF;

  DELETE FROM identity.team_members AS membership
  WHERE membership.team_id = remove_team_member.team_id
    AND membership.user_id = remove_team_member.user_id;
  RETURN FOUND;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.remove_team_member(bytea, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.remove_team_member(bytea, uuid, uuid) TO upmo_app;

-- team_create lets a live team manager create a team without direct table writes.
-- +goose StatementBegin
CREATE FUNCTION identity.team_create(actor_token_hash bytea, name text)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_perms text[];
  v_name text;
  v_id uuid;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT identity.role_permissions(role.key, role.permissions)
  INTO v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF NOT ('teams:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only team managers manage teams' USING ERRCODE = '42501';
  END IF;

  v_name := pg_catalog.regexp_replace(team_create.name, '^[[:space:] ]+|[[:space:] ]+$', '', 'g');
  IF v_name IS NULL OR v_name = '' OR pg_catalog.char_length(v_name) > 64 THEN
    RAISE EXCEPTION 'identity: bad team name' USING ERRCODE = '22023';
  END IF;

  INSERT INTO identity.teams AS team (name) VALUES (v_name) RETURNING team.id INTO v_id;
  RETURN v_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.team_create(bytea, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.team_create(bytea, text) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.member_disable(
  actor_token_hash bytea,
  target_user_id uuid,
  at timestamptz
)
RETURNS void
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_perms text[];
  v_target_role_key text;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;
  IF at IS NULL THEN
    RAISE EXCEPTION 'identity: disable timestamp required' USING ERRCODE = '22004';
  END IF;

  SELECT person.id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF v_actor_id = target_user_id THEN
    RAISE EXCEPTION 'identity: you cannot do this to yourself' USING ERRCODE = '42501';
  END IF;

  SELECT COALESCE(role.key, '')
  INTO v_target_role_key
  FROM identity.users AS person
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE person.id = target_user_id
    AND person.disabled_at IS NULL
  FOR NO KEY UPDATE OF person;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such member' USING ERRCODE = 'P0002';
  END IF;

  IF NOT ('members:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only owners and admins manage members' USING ERRCODE = '42501';
  END IF;

  IF v_target_role_key = 'owner' AND NOT ('owners:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only an owner can change the owner role' USING ERRCODE = '42501';
  END IF;

  DELETE FROM identity.passkeys WHERE user_id = target_user_id;
  DELETE FROM identity.sessions WHERE user_id = target_user_id;
  DELETE FROM identity.team_members WHERE user_id = target_user_id;
  DELETE FROM identity.shares WHERE user_id = target_user_id;
  DELETE FROM identity.webauthn_challenges WHERE user_id = target_user_id;
  DELETE FROM identity.sign_in_links WHERE user_id = target_user_id AND used_at IS NULL;
  DELETE FROM identity.api_tokens WHERE user_id = target_user_id;

  UPDATE identity.users
  SET disabled_at = at,
      password_hash = NULL,
      webauthn_handle = NULL
  WHERE id = target_user_id AND disabled_at IS NULL;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.member_disable(bytea, uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.member_disable(bytea, uuid, timestamptz) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.avatar_set(
  actor_token_hash bytea,
  p_avatar_key text
)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_old_key text;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF p_avatar_key IS NOT NULL THEN
    IF pg_catalog.btrim(p_avatar_key) = ''
       OR pg_catalog.char_length(p_avatar_key) > 64
       OR p_avatar_key !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
      RAISE EXCEPTION 'identity: invalid avatar key' USING ERRCODE = '22023';
    END IF;
  END IF;

  SELECT person.id, person.avatar_key
  INTO v_actor_id, v_old_key
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL
  FOR NO KEY UPDATE OF person;

  IF v_actor_id IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  UPDATE identity.users
  SET avatar_key = p_avatar_key
  WHERE id = v_actor_id;

  RETURN COALESCE(v_old_key, '');
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.avatar_set(bytea, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.avatar_set(bytea, text) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.member_invite(
  actor_token_hash bytea,
  p_email text,              -- sealed value
  p_email_hashes bytea[],    -- index hashes under every loaded key, current key's first
  p_email_hash_key_id text,  -- id of the key that made p_email_hashes[1]
  p_name text,
  p_role_id uuid,
  p_handle bytea,
  p_link_hash bytea,
  p_now timestamptz,
  p_expires timestamptz
)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_perms text[];
  v_target_role_key text;
  v_owner_role_id uuid;
  v_user_id uuid;
  v_disabled boolean;
  v_matches integer;
BEGIN
  -- The address itself is sealed, so only its presence can be checked here;
  -- the caller has already validated the address.
  IF p_email IS NULL OR pg_catalog.btrim(p_email) = ''
     OR p_email_hashes IS NULL OR pg_catalog.cardinality(p_email_hashes) < 1
     OR pg_catalog.octet_length(p_email_hashes[1]) IS DISTINCT FROM 32
     OR p_email_hash_key_id IS NULL OR pg_catalog.btrim(p_email_hash_key_id) = '' THEN
    RAISE EXCEPTION 'identity: a person needs an email address, a name and a role' USING ERRCODE = '23514';
  END IF;

  IF actor_token_hash IS NULL THEN
    PERFORM pg_catalog.pg_advisory_xact_lock(7480198);
    IF EXISTS (SELECT 1 FROM identity.users) THEN
      RAISE EXCEPTION 'identity: someone already exists' USING ERRCODE = '42501';
    END IF;
    SELECT role.id INTO v_owner_role_id FROM identity.roles AS role WHERE role.key = 'owner';
    INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, joined_at, webauthn_handle)
    VALUES (p_email, p_email_hashes[1], p_email_hash_key_id, p_name, v_owner_role_id, p_now, p_now, p_handle)
    RETURNING id INTO v_user_id;

    INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
    VALUES (p_link_hash, v_user_id, 'bootstrap', p_expires);

    RETURN v_user_id;
  END IF;

  SELECT identity.role_permissions(role.key, role.permissions)
  INTO v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF p_name IS NULL OR pg_catalog.btrim(p_name) = '' OR p_role_id IS NULL THEN
    RAISE EXCEPTION 'identity: a person needs an email address, a name and a role' USING ERRCODE = '23514';
  END IF;

  SELECT COALESCE(role.key, '')
  INTO v_target_role_key
  FROM identity.roles AS role
  WHERE role.id = p_role_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: a person needs an email address, a name and a role' USING ERRCODE = '23503';
  END IF;

  IF NOT ('members:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only owners and admins manage members' USING ERRCODE = '42501';
  END IF;

  IF v_target_role_key = 'owner' AND NOT ('owners:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only an owner can change the owner role' USING ERRCODE = '42501';
  END IF;

  -- ON CONFLICT cannot match "any of the hashes", so the row is found first.
  -- A concurrent insert of the same address loses the race as a unique
  -- violation on users_email_hash, which the caller maps the same way.
  -- Rows for the same address under different keys cannot be told apart, so
  -- more than one match is refused rather than picking one.
  PERFORM 1 FROM identity.users AS person
  WHERE person.email_hash = ANY (p_email_hashes)
  FOR UPDATE;
  GET DIAGNOSTICS v_matches = ROW_COUNT;
  IF v_matches > 1 THEN
    RAISE EXCEPTION 'identity: already a member' USING ERRCODE = '23505';
  END IF;

  SELECT person.id, person.disabled_at IS NOT NULL INTO v_user_id, v_disabled
  FROM identity.users AS person
  WHERE person.email_hash = ANY (p_email_hashes)
  FOR UPDATE;
  IF FOUND AND NOT v_disabled THEN
    RAISE EXCEPTION 'identity: already a member' USING ERRCODE = '23505';
  ELSIF FOUND THEN
    UPDATE identity.users
    SET email = p_email, email_hash = p_email_hashes[1], email_hash_key_id = p_email_hash_key_id,
        name = p_name, role_id = p_role_id, invited_at = p_now, joined_at = NULL, disabled_at = NULL,
        webauthn_handle = p_handle, password_hash = NULL, passkey_offered_at = NULL, external_id = NULL,
        avatar_key = NULL
    WHERE id = v_user_id;
  ELSE
    INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, webauthn_handle)
    VALUES (p_email, p_email_hashes[1], p_email_hash_key_id, p_name, p_role_id, p_now, p_handle)
    RETURNING id INTO v_user_id;
  END IF;

  DELETE FROM identity.passkeys WHERE user_id = v_user_id;
  DELETE FROM identity.sessions WHERE user_id = v_user_id;
  DELETE FROM identity.team_members WHERE user_id = v_user_id;
  DELETE FROM identity.shares WHERE user_id = v_user_id;
  DELETE FROM identity.webauthn_challenges WHERE user_id = v_user_id;
  DELETE FROM identity.sign_in_links WHERE user_id = v_user_id AND used_at IS NULL;
  DELETE FROM identity.api_tokens WHERE user_id = v_user_id;

  INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
  VALUES (p_link_hash, v_user_id, 'invite', p_expires);

  RETURN v_user_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.member_invite(bytea, text, bytea[], text, text, uuid, bytea, bytea, timestamptz, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.member_invite(bytea, text, bytea[], text, text, uuid, bytea, bytea, timestamptz, timestamptz) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.token_create(
  actor_token_hash bytea,
  p_name text,
  p_token_hash bytea,
  p_scope text[],
  p_created timestamptz,
  p_expires timestamptz
)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_perms text[];
  v_token_id uuid;
  v_clean_name text;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  v_clean_name := pg_catalog.btrim(COALESCE(p_name, ''));
  IF v_clean_name = '' OR pg_catalog.char_length(v_clean_name) > 64 THEN
    RAISE EXCEPTION 'identity: a token needs a name' USING ERRCODE = '22023';
  END IF;

  IF p_scope IS NULL OR COALESCE(pg_catalog.array_length(p_scope, 1), 0) = 0 THEN
    RAISE EXCEPTION 'identity: a token cannot hold that permission' USING ERRCODE = '42501';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM pg_catalog.unnest(p_scope) AS s
    WHERE s IS NULL
       OR NOT (s = ANY (ARRAY[
         'task:create', 'task:view', 'task:edit', 'task:share',
         'chat:create', 'chat:view', 'chat:edit', 'chat:share',
         'folder:create', 'folder:view', 'folder:edit', 'folder:share',
         'automation:create', 'automation:view', 'automation:edit', 'automation:share',
         'agent:create', 'agent:view', 'agent:edit', 'agent:share',
         'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share',
         'audit:view'
       ]::text[]))
       OR NOT (s = ANY (v_actor_perms))
  ) THEN
    RAISE EXCEPTION 'identity: a token cannot hold that permission' USING ERRCODE = '42501';
  END IF;

  IF (SELECT pg_catalog.count(DISTINCT s) FROM pg_catalog.unnest(p_scope) AS s)
     <> pg_catalog.array_length(p_scope, 1) THEN
    RAISE EXCEPTION 'identity: a token cannot hold that permission' USING ERRCODE = '42501';
  END IF;

  INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
  VALUES (v_actor_id, v_clean_name, p_token_hash, p_scope, p_created, p_expires)
  RETURNING id INTO v_token_id;

  RETURN v_token_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.token_create(bytea, text, bytea, text[], timestamptz, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.token_create(bytea, text, bytea, text[], timestamptz, timestamptz) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.role_create(
  actor_token_hash bytea,
  p_name text,
  p_permissions text[]
)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_role_id uuid;
  v_actor_perms text[];
  v_clean text;
  v_role_id uuid;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id, person.role_id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_role_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF NOT ('roles:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: you cannot manage roles' USING ERRCODE = '42501';
  END IF;

  v_clean := pg_catalog.btrim(COALESCE(p_name, ''));
  IF v_clean = '' OR pg_catalog.char_length(v_clean) > 64 THEN
    RAISE EXCEPTION 'identity: a role needs a name and at least one permission' USING ERRCODE = '23514';
  END IF;

  IF p_permissions IS NULL OR pg_catalog.cardinality(p_permissions) = 0 THEN
    RAISE EXCEPTION 'identity: a role needs a name and at least one permission' USING ERRCODE = '23514';
  END IF;

  -- Unknown names, NULL elements and owners:manage are dropped by the
  -- NULL-key branch, so a cardinality mismatch refuses them; a distinct
  -- count below the cardinality refuses duplicates.
  IF pg_catalog.cardinality(identity.role_permissions(NULL, p_permissions)) <> pg_catalog.cardinality(p_permissions)
     OR (SELECT pg_catalog.count(DISTINCT s) FROM pg_catalog.unnest(p_permissions) AS s)
        <> pg_catalog.cardinality(p_permissions) THEN
    RAISE EXCEPTION 'identity: a role needs a name and at least one permission' USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.unnest(p_permissions) AS s
    WHERE NOT (s = ANY (v_actor_perms))
  ) THEN
    RAISE EXCEPTION 'identity: you cannot grant a permission you do not hold' USING ERRCODE = '42501';
  END IF;

  INSERT INTO identity.roles (name, permissions)
  VALUES (v_clean, p_permissions)
  RETURNING id INTO v_role_id;

  RETURN v_role_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.role_create(bytea, text, text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.role_create(bytea, text, text[]) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.role_change(
  actor_token_hash bytea,
  p_role_id uuid,
  p_name text,
  p_permissions text[]
)
RETURNS text[]
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_role_id uuid;
  v_actor_perms text[];
  v_key text;
  v_stored_perms text[];
  v_old_perms text[];
  v_clean text;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id, person.role_id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_role_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF NOT ('roles:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: you cannot manage roles' USING ERRCODE = '42501';
  END IF;

  v_clean := pg_catalog.btrim(COALESCE(p_name, ''));
  IF v_clean = '' OR pg_catalog.char_length(v_clean) > 64 THEN
    RAISE EXCEPTION 'identity: a role needs a name and at least one permission' USING ERRCODE = '23514';
  END IF;

  IF p_permissions IS NULL OR pg_catalog.cardinality(p_permissions) = 0 THEN
    RAISE EXCEPTION 'identity: a role needs a name and at least one permission' USING ERRCODE = '23514';
  END IF;

  IF pg_catalog.cardinality(identity.role_permissions(NULL, p_permissions)) <> pg_catalog.cardinality(p_permissions)
     OR (SELECT pg_catalog.count(DISTINCT s) FROM pg_catalog.unnest(p_permissions) AS s)
        <> pg_catalog.cardinality(p_permissions) THEN
    RAISE EXCEPTION 'identity: a role needs a name and at least one permission' USING ERRCODE = '23514';
  END IF;

  SELECT role.key, role.permissions
  INTO v_key, v_stored_perms
  FROM identity.roles AS role
  WHERE role.id = p_role_id
  FOR NO KEY UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such role' USING ERRCODE = 'P0002';
  END IF;

  IF v_key IS NOT NULL THEN
    RAISE EXCEPTION 'identity: a system role cannot be changed' USING ERRCODE = '42501';
  END IF;

  IF p_role_id = v_actor_role_id THEN
    RAISE EXCEPTION 'identity: you cannot do this to yourself' USING ERRCODE = '42501';
  END IF;

  -- Only added permissions must be held; removing one the actor lacks is fine.
  v_old_perms := identity.role_permissions(NULL, v_stored_perms);
  IF EXISTS (
    SELECT 1 FROM pg_catalog.unnest(p_permissions) AS s
    WHERE NOT (s = ANY (v_actor_perms)) AND NOT (s = ANY (v_old_perms))
  ) THEN
    RAISE EXCEPTION 'identity: you cannot grant a permission you do not hold' USING ERRCODE = '42501';
  END IF;

  UPDATE identity.roles
  SET name = v_clean, permissions = p_permissions
  WHERE id = p_role_id AND key IS NULL;

  RETURN v_stored_perms;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.role_change(bytea, uuid, text, text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.role_change(bytea, uuid, text, text[]) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.role_delete(
  actor_token_hash bytea,
  p_role_id uuid
)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_role_id uuid;
  v_actor_perms text[];
  v_key text;
  v_holders bigint;
  v_shares bigint;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id, person.role_id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_role_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF NOT ('roles:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: you cannot manage roles' USING ERRCODE = '42501';
  END IF;

  SELECT role.key
  INTO v_key
  FROM identity.roles AS role
  WHERE role.id = p_role_id
  FOR UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such role' USING ERRCODE = 'P0002';
  END IF;

  IF v_key IS NOT NULL THEN
    RAISE EXCEPTION 'identity: a system role cannot be changed' USING ERRCODE = '42501';
  END IF;

  IF p_role_id = v_actor_role_id THEN
    RAISE EXCEPTION 'identity: you cannot do this to yourself' USING ERRCODE = '42501';
  END IF;

  -- Holders are returned, not raised, so the caller can name the count and
  -- the transaction stays usable.
  SELECT count(*) INTO v_holders FROM identity.users WHERE role_id = p_role_id;
  IF v_holders > 0 THEN
    RETURN v_holders;
  END IF;

  SELECT count(*) INTO v_shares FROM identity.shares WHERE grantee_role_id = p_role_id;
  IF v_shares > 0 THEN
    RAISE EXCEPTION 'identity: a role used by a share cannot be deleted' USING ERRCODE = '23503';
  END IF;

  DELETE FROM identity.roles WHERE id = p_role_id AND key IS NULL;

  RETURN 0;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.role_delete(bytea, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.role_delete(bytea, uuid) TO upmo_app;

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

-- Definers for the serving roles. Each returns the least the caller needs:
-- ids, one time zone, a name, a flag. None returns email or password_hash.

-- +goose StatementBegin
CREATE FUNCTION identity.workspace_time_zone()
RETURNS text
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT COALESCE(NULLIF((SELECT w.time_zone FROM identity.workspace AS w WHERE w.id), ''), 'UTC')
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.workspace_time_zone() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.workspace_time_zone() TO upmo_app, upmo_gateway;

-- +goose StatementBegin
CREATE FUNCTION identity.settings_manager_ids()
RETURNS SETOF uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT u.id
  FROM identity.users AS u
  JOIN identity.roles AS r ON r.id = u.role_id
  WHERE u.joined_at IS NOT NULL
    AND u.disabled_at IS NULL
    AND 'settings:manage' = ANY (identity.role_permissions(r.key, r.permissions))
  ORDER BY u.id
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.settings_manager_ids() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.settings_manager_ids() TO upmo_app, upmo_gateway;

-- Password sign-in finds the person by an email hash before any session
-- exists. The sealed password is not returned.
-- +goose StatementBegin
CREATE FUNCTION identity.password_account(email_hash bytea[])
RETURNS TABLE (id uuid, name text, found boolean)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT u.id, u.name, true
  FROM identity.users AS u
  WHERE u.email_hash = ANY (password_account.email_hash)
    AND u.disabled_at IS NULL
  LIMIT 1
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.password_account(bytea[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.password_account(bytea[]) TO upmo_app;

-- Discoverable passkey sign-in: the one enabled account behind a credential
-- and its handle. There is no session yet.
-- +goose StatementBegin
CREATE FUNCTION identity.account_by_credential(credential_id bytea, webauthn_handle bytea)
RETURNS TABLE (id uuid, name text, webauthn_handle bytea)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT person.id, person.name, person.webauthn_handle
  FROM identity.passkeys AS passkey
  JOIN identity.users AS person ON person.id = passkey.user_id
  WHERE passkey.credential_id = account_by_credential.credential_id
    AND person.webauthn_handle = account_by_credential.webauthn_handle
    AND person.disabled_at IS NULL
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.account_by_credential(bytea, bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.account_by_credential(bytea, bytea) TO upmo_app;

-- Whether the session's own person has a password. The person comes from
-- the session hash, never from upmo.user_id.
-- +goose StatementBegin
CREATE FUNCTION identity.has_password(actor_token_hash bytea)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  v_has boolean;
BEGIN
  SELECT u.password_hash IS NOT NULL INTO v_has
  FROM identity.sessions AS s
  JOIN identity.users AS u ON u.id = s.user_id
  WHERE s.token_hash = actor_token_hash
    AND u.disabled_at IS NULL;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;
  RETURN v_has;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.has_password(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.has_password(bytea) TO upmo_app;

-- Whether an enabled person has a password, by id, for the paths with no
-- session: a link redeem and the credential change lock. A disabled or
-- unknown person raises.
-- +goose StatementBegin
CREATE FUNCTION identity.has_password_of(user_id uuid)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  v_has boolean;
BEGIN
  SELECT u.password_hash IS NOT NULL INTO v_has
  FROM identity.users AS u
  WHERE u.id = has_password_of.user_id
    AND u.disabled_at IS NULL;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such person' USING ERRCODE = '42501';
  END IF;
  RETURN v_has;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.has_password_of(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.has_password_of(uuid) TO upmo_app;

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

DROP FUNCTION IF EXISTS identity.has_password_of(uuid);
DROP FUNCTION IF EXISTS identity.has_password(bytea);
DROP FUNCTION IF EXISTS identity.account_by_credential(bytea, bytea);
DROP FUNCTION IF EXISTS identity.password_account(bytea[]);
DROP FUNCTION IF EXISTS identity.settings_manager_ids();
DROP FUNCTION IF EXISTS identity.workspace_time_zone();
DROP FUNCTION IF EXISTS identity.sso_sign_in_state(uuid);
DROP POLICY IF EXISTS workspace_update ON identity.workspace;
DROP POLICY IF EXISTS workspace_select ON identity.workspace;
DROP POLICY IF EXISTS workspace_owner ON identity.workspace;
DROP FUNCTION IF EXISTS identity.workspace_owner();
DROP FUNCTION IF EXISTS identity.may_manage_provider(uuid, text);
DROP FUNCTION IF EXISTS identity.member_providers_allowed();
DROP FUNCTION IF EXISTS identity.role_delete(bytea, uuid);
DROP FUNCTION IF EXISTS identity.role_change(bytea, uuid, text, text[]);
DROP FUNCTION IF EXISTS identity.role_create(bytea, text, text[]);
DROP FUNCTION IF EXISTS identity.token_create(bytea, text, bytea, text[], timestamptz, timestamptz);
DROP FUNCTION IF EXISTS identity.member_invite(bytea, text, bytea[], text, text, uuid, bytea, bytea, timestamptz, timestamptz);
DROP FUNCTION IF EXISTS identity.avatar_set(bytea, text);
DROP FUNCTION IF EXISTS identity.member_disable(bytea, uuid, timestamptz);
DROP FUNCTION IF EXISTS identity.names_of(bytea, uuid[], uuid[]);
DROP FUNCTION IF EXISTS identity.role_directory(bytea);
DROP FUNCTION IF EXISTS identity.object_grants(bytea, text, uuid);
DROP FUNCTION IF EXISTS identity.replace_object_grants(bytea, text, uuid, jsonb);
DROP FUNCTION IF EXISTS identity.add_team_member(bytea, uuid, uuid);
DROP FUNCTION IF EXISTS identity.remove_team_member(bytea, uuid, uuid);
DROP FUNCTION IF EXISTS identity.team_create(bytea, text);
DROP FUNCTION IF EXISTS identity.member_set_role(bytea, uuid, uuid);
DROP FUNCTION IF EXISTS identity.runner_access(uuid);
DROP FUNCTION IF EXISTS identity.role_permissions(text, text[]);
DROP FUNCTION IF EXISTS identity.teams_of(uuid);
DROP FUNCTION IF EXISTS identity.may_manage(uuid, text);
DROP FUNCTION IF EXISTS identity.may_use(uuid, text, uuid[]);
DROP FUNCTION IF EXISTS identity.can_use(uuid, uuid[], uuid, text, uuid[]);
DROP FUNCTION IF EXISTS identity.challenge_take(bytea, timestamptz);
DROP FUNCTION IF EXISTS identity.link_spend(bytea, timestamptz);
DROP FUNCTION IF EXISTS identity.link_find(bytea, timestamptz);
DROP FUNCTION IF EXISTS identity.token_find(bytea);
DROP FUNCTION IF EXISTS identity.session_find(bytea);
DROP FUNCTION IF EXISTS identity.protect(regclass);
DROP FUNCTION IF EXISTS identity.may(text, text, uuid, text, uuid, uuid);
REVOKE ALL ON ALL TABLES IN SCHEMA identity FROM upmo_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA identity FROM upmo_app;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA identity FROM upmo_app;
REVOKE USAGE ON SCHEMA identity FROM upmo_app;
DROP TABLE IF EXISTS identity.provision_nonces;
DROP TABLE IF EXISTS identity.workspace;
DROP TABLE IF EXISTS identity.connector_grants;
DROP TABLE IF EXISTS identity.api_tokens;
DROP TABLE IF EXISTS identity.throttle;
DROP TABLE IF EXISTS identity.password_verify_window;
DROP TABLE IF EXISTS identity.sign_in_links;
DROP TABLE IF EXISTS identity.webauthn_challenges;
DROP TABLE IF EXISTS identity.passkeys;
DROP TRIGGER IF EXISTS users_delete_sessions ON identity.users;
DROP FUNCTION IF EXISTS identity.delete_sessions_on_disable();
DROP TABLE IF EXISTS identity.sessions;
DROP TABLE IF EXISTS identity.shares;
DROP TABLE IF EXISTS identity.team_members;
DROP TABLE IF EXISTS identity.teams;
DROP TABLE IF EXISTS identity.users;
DROP TABLE IF EXISTS identity.roles;
DROP FUNCTION IF EXISTS identity.keep_system_roles();
DROP FUNCTION IF EXISTS identity.team_member_has_joined();
DROP FUNCTION IF EXISTS identity.keep_joined_owner();
