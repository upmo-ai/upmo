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
       WHERE person.role = 'owner'
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

CREATE TABLE identity.users (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  email text NOT NULL,
  name text NOT NULL,
  external_id text,
  role text NOT NULL,
  invited_at timestamptz,
  joined_at timestamptz,
  disabled_at timestamptz,
  CONSTRAINT users_role_check CHECK (role IN ('owner', 'admin', 'member', 'viewer'))
);

CREATE UNIQUE INDEX users_email_ci ON identity.users (lower(email));

CREATE UNIQUE INDEX users_external_id ON identity.users (external_id) WHERE external_id IS NOT NULL;

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
  level text NOT NULL,
  granted_by uuid NOT NULL REFERENCES identity.users (id),
  CONSTRAINT shares_level_check CHECK (level IN ('editor', 'viewer')),
  CONSTRAINT shares_grantee_check CHECK (
    (user_id IS NOT NULL AND team_id IS NULL)
    OR (user_id IS NULL AND team_id IS NOT NULL)
  )
);

CREATE UNIQUE INDEX shares_user_once ON identity.shares (object_type, object_id, user_id) WHERE user_id IS NOT NULL;

CREATE UNIQUE INDEX shares_team_once ON identity.shares (object_type, object_id, team_id) WHERE team_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS identity.shares;
DROP TABLE IF EXISTS identity.principals;
DROP TABLE IF EXISTS identity.team_members;
DROP TABLE IF EXISTS identity.teams;
DROP TABLE IF EXISTS identity.users;
DROP FUNCTION IF EXISTS identity.team_member_has_joined();
DROP FUNCTION IF EXISTS identity.keep_joined_owner();
