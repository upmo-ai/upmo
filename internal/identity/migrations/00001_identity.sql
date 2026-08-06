-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION identity.keep_joined_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
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
  webauthn_handle bytea
);

CREATE UNIQUE INDEX users_email_ci ON identity.users (lower(email));

CREATE UNIQUE INDEX users_external_id ON identity.users (external_id) WHERE external_id IS NOT NULL;

CREATE UNIQUE INDEX users_webauthn_handle ON identity.users (webauthn_handle) WHERE webauthn_handle IS NOT NULL;

CREATE CONSTRAINT TRIGGER users_keep_joined_owner
AFTER INSERT OR UPDATE OR DELETE ON identity.users
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
  CONSTRAINT sessions_sign_in_method_check CHECK (sign_in_method IN ('passkey', 'link'))
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
  token_hash bytea PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  purpose text NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  CONSTRAINT sign_in_links_purpose_check CHECK (purpose IN ('invite', 'recover', 'bootstrap'))
);

CREATE TABLE identity.throttle (
  key text PRIMARY KEY,
  tokens double precision NOT NULL,
  updated_at timestamptz NOT NULL
);

-- +goose Down
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
