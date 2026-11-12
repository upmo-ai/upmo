-- Provider accounts join the shared-configuration model. Every account has an
-- owner except a house account, which stays ownerless and visible to everyone.
-- visibility and team_ids replace gateway.provider_account_teams. Row policies
-- and the gateway call the identity rules, so a person who can use an account
-- and a person who may manage one cannot drift apart. settings:manage is the
-- only permission that manages a shared account.

-- +goose Up

ALTER TABLE gateway.provider_accounts
  ADD COLUMN visibility text NOT NULL DEFAULT 'private',
  ADD COLUMN team_ids uuid[] NOT NULL DEFAULT '{}';

-- House accounts are the workspace's, for everyone.
UPDATE gateway.provider_accounts
SET visibility = 'everyone'
WHERE house;

-- A shared account with no owner and team rows was shared with those teams.
UPDATE gateway.provider_accounts AS a
SET visibility = 'team',
    team_ids = (
      SELECT COALESCE(array_agg(t.team_id ORDER BY t.team_id), '{}')
      FROM gateway.provider_account_teams AS t
      WHERE t.account_id = a.id
    )
WHERE NOT a.house
  AND a.owner_user_id IS NULL
  AND EXISTS (
    SELECT 1 FROM gateway.provider_account_teams AS t WHERE t.account_id = a.id
  );

-- A shared account with no owner and no team rows was for the whole workspace.
UPDATE gateway.provider_accounts AS a
SET visibility = 'everyone'
WHERE NOT a.house
  AND a.owner_user_id IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM gateway.provider_account_teams AS t WHERE t.account_id = a.id
  );

-- An account that already had an owner was personal.
UPDATE gateway.provider_accounts
SET visibility = 'private'
WHERE owner_user_id IS NOT NULL;

-- Their creator was never recorded. The earliest joined, enabled person with
-- the system owner role becomes the owner. House accounts stay ownerless.
-- +goose StatementBegin
DO $$
DECLARE
  fallback uuid;
BEGIN
  IF EXISTS (
    SELECT 1 FROM gateway.provider_accounts
    WHERE owner_user_id IS NULL AND NOT house
  ) THEN
    SELECT person.id INTO fallback
    FROM identity.users AS person
    JOIN identity.roles AS role ON role.id = person.role_id
    WHERE role.key = 'owner'
      AND person.joined_at IS NOT NULL
      AND person.disabled_at IS NULL
    ORDER BY person.joined_at, person.id
    LIMIT 1;
    IF fallback IS NULL THEN
      RAISE EXCEPTION 'provider accounts need a joined owner';
    END IF;
    UPDATE gateway.provider_accounts
    SET owner_user_id = fallback
    WHERE owner_user_id IS NULL AND NOT house;
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE gateway.provider_accounts
  DROP CONSTRAINT provider_accounts_house_owner_check,
  ADD CONSTRAINT provider_accounts_visibility_check CHECK (visibility IN ('private', 'team', 'everyone')),
  ADD CONSTRAINT provider_accounts_team_ids_check CHECK ((visibility = 'team') = (cardinality(team_ids) > 0)),
  ADD CONSTRAINT provider_accounts_house_owner_check CHECK (house = (owner_user_id IS NULL)),
  ADD CONSTRAINT provider_accounts_house_visibility_check CHECK (NOT house OR visibility = 'everyone');

-- A private name is unique per person. A shared name is unique among shared
-- and house accounts.
DROP INDEX gateway.provider_accounts_owner_name;
CREATE UNIQUE INDEX provider_accounts_owner_name_private
  ON gateway.provider_accounts (owner_user_id, name)
  WHERE visibility = 'private';
CREATE UNIQUE INDEX provider_accounts_owner_name_shared
  ON gateway.provider_accounts (name)
  WHERE visibility <> 'private';

CREATE INDEX provider_accounts_team_ids ON gateway.provider_accounts USING gin (team_ids);

DROP POLICY provider_accounts_select ON gateway.provider_accounts;
DROP POLICY provider_accounts_insert ON gateway.provider_accounts;
DROP POLICY provider_accounts_update ON gateway.provider_accounts;
DROP POLICY provider_accounts_delete ON gateway.provider_accounts;
DROP POLICY provider_accounts_shared_insert ON gateway.provider_accounts;
DROP POLICY provider_accounts_shared_update ON gateway.provider_accounts;
DROP POLICY provider_accounts_shared_delete ON gateway.provider_accounts;
DROP POLICY provider_models_select ON gateway.provider_models;
DROP POLICY provider_models_insert ON gateway.provider_models;
DROP POLICY provider_models_delete ON gateway.provider_models;
DROP POLICY provider_models_shared_insert ON gateway.provider_models;
DROP POLICY provider_models_shared_delete ON gateway.provider_models;
DROP POLICY credentials_own ON gateway.credentials;
DROP POLICY credentials_shared ON gateway.credentials;
DROP POLICY provider_account_teams_service ON gateway.provider_account_teams;
DROP POLICY provider_account_teams_select ON gateway.provider_account_teams;
DROP POLICY provider_account_teams_insert ON gateway.provider_account_teams;
DROP POLICY provider_account_teams_delete ON gateway.provider_account_teams;

-- A person sees an account they can use, and a shared account they may manage.
-- They do not see another person's private account. House stays readable.
CREATE POLICY provider_accounts_select ON gateway.provider_accounts
  FOR SELECT
  USING (
    identity.may_use(owner_user_id, visibility, team_ids)
    OR identity.may_manage(owner_user_id, visibility)
  );

-- The writer is the owner. Private needs no extra permission. Team and
-- everyone need settings:manage, which may_manage reads. House is not created
-- here.
CREATE POLICY provider_accounts_insert ON gateway.provider_accounts
  FOR INSERT
  WITH CHECK (
    NOT house
    AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND identity.may_manage(owner_user_id, visibility)
  );

-- House stays with the environment. Taking a shared account private is
-- may_manage of the new row, which holds for private only when the writer is
-- the owner.
CREATE POLICY provider_accounts_update ON gateway.provider_accounts
  FOR UPDATE
  USING (NOT house AND identity.may_manage(owner_user_id, visibility))
  WITH CHECK (NOT house AND identity.may_manage(owner_user_id, visibility));

CREATE POLICY provider_accounts_delete ON gateway.provider_accounts
  FOR DELETE
  USING (NOT house AND identity.may_manage(owner_user_id, visibility));

-- A model list follows its account: visible where the account is, writable
-- where the account may be managed.
CREATE POLICY provider_models_select ON gateway.provider_models
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
  ));

CREATE POLICY provider_models_insert ON gateway.provider_models
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND identity.may_manage(a.owner_user_id, a.visibility)
  ));

CREATE POLICY provider_models_delete ON gateway.provider_models
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND identity.may_manage(a.owner_user_id, a.visibility)
  ));

-- A person who only uses a shared account has no policy on its key. House
-- keys stay out of reach. The owner of a private account, and anyone who may
-- manage a shared one, can read and replace the key.
CREATE POLICY credentials_manage ON gateway.credentials
  FOR ALL
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref
      AND NOT a.house
      AND identity.may_manage(a.owner_user_id, a.visibility)
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref
      AND NOT a.house
      AND identity.may_manage(a.owner_user_id, a.visibility)
  ));

DROP FUNCTION gateway.team_shared(uuid);
DROP FUNCTION gateway.my_teams();
DROP FUNCTION gateway.may_share();
DROP TABLE gateway.provider_account_teams;

GRANT UPDATE (visibility, team_ids) ON gateway.provider_accounts TO upmo_app;

-- +goose Down

DROP POLICY IF EXISTS credentials_manage ON gateway.credentials;
DROP POLICY IF EXISTS provider_models_delete ON gateway.provider_models;
DROP POLICY IF EXISTS provider_models_insert ON gateway.provider_models;
DROP POLICY IF EXISTS provider_models_select ON gateway.provider_models;
DROP POLICY IF EXISTS provider_accounts_delete ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_update ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_insert ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_select ON gateway.provider_accounts;

ALTER TABLE gateway.provider_accounts
  DROP CONSTRAINT provider_accounts_house_visibility_check,
  DROP CONSTRAINT provider_accounts_house_owner_check,
  DROP CONSTRAINT provider_accounts_team_ids_check,
  DROP CONSTRAINT provider_accounts_visibility_check;

CREATE TABLE gateway.provider_account_teams (
  account_id uuid NOT NULL REFERENCES gateway.provider_accounts (id) ON DELETE CASCADE,
  team_id uuid NOT NULL REFERENCES identity.teams (id) ON DELETE CASCADE,
  PRIMARY KEY (account_id, team_id)
);

CREATE INDEX provider_account_teams_team ON gateway.provider_account_teams (team_id);

INSERT INTO gateway.provider_account_teams (account_id, team_id)
SELECT a.id, tid
FROM gateway.provider_accounts AS a
CROSS JOIN LATERAL unnest(a.team_ids) AS tid
WHERE a.visibility = 'team';

-- A shared account had no owner in the old shape. A private one keeps its owner.
UPDATE gateway.provider_accounts
SET owner_user_id = NULL
WHERE visibility <> 'private';

DROP INDEX gateway.provider_accounts_team_ids;
DROP INDEX gateway.provider_accounts_owner_name_shared;
DROP INDEX gateway.provider_accounts_owner_name_private;
CREATE UNIQUE INDEX provider_accounts_owner_name
  ON gateway.provider_accounts (COALESCE(owner_user_id, '00000000-0000-0000-0000-000000000000'::uuid), name);

ALTER TABLE gateway.provider_accounts
  DROP COLUMN team_ids,
  DROP COLUMN visibility;

ALTER TABLE gateway.provider_accounts
  ADD CONSTRAINT provider_accounts_house_owner_check CHECK (NOT house OR owner_user_id IS NULL);

-- +goose StatementBegin
CREATE FUNCTION gateway.may_share()
RETURNS boolean
LANGUAGE sql
STABLE
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT COALESCE(
    'teams:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')),
    false
  );
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION gateway.my_teams()
RETURNS uuid[]
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT COALESCE(array_agg(m.team_id), '{}'::uuid[])
  FROM identity.team_members AS m
  WHERE m.user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION gateway.team_shared(account uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT EXISTS (SELECT 1 FROM gateway.provider_account_teams AS t WHERE t.account_id = account);
$$;
-- +goose StatementEnd

ALTER TABLE gateway.provider_account_teams ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.provider_account_teams FORCE ROW LEVEL SECURITY;

CREATE POLICY provider_account_teams_service ON gateway.provider_account_teams
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY provider_account_teams_select ON gateway.provider_account_teams
  FOR SELECT
  USING (team_id = ANY (gateway.my_teams()) OR gateway.may_share());

CREATE POLICY provider_account_teams_insert ON gateway.provider_account_teams
  FOR INSERT
  WITH CHECK (
    gateway.may_share()
    AND EXISTS (
      SELECT 1 FROM gateway.provider_accounts AS a
      WHERE a.id = account_id AND a.owner_user_id IS NULL AND NOT a.house
    )
  );

CREATE POLICY provider_account_teams_delete ON gateway.provider_account_teams
  FOR DELETE
  USING (
    gateway.may_share()
    AND EXISTS (
      SELECT 1 FROM gateway.provider_accounts AS a
      WHERE a.id = account_id AND a.owner_user_id IS NULL AND NOT a.house
    )
  );

CREATE POLICY provider_accounts_select ON gateway.provider_accounts
  FOR SELECT
  USING (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    OR (
      owner_user_id IS NULL
      AND (
        NOT gateway.team_shared(id)
        OR gateway.may_share()
        OR EXISTS (
          SELECT 1 FROM gateway.provider_account_teams AS t
          WHERE t.account_id = id AND t.team_id = ANY (gateway.my_teams())
        )
      )
    )
  );

CREATE POLICY provider_accounts_insert ON gateway.provider_accounts
  FOR INSERT
  WITH CHECK (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND NOT house
  );

CREATE POLICY provider_accounts_update ON gateway.provider_accounts
  FOR UPDATE
  USING (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  WITH CHECK (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid AND NOT house);

CREATE POLICY provider_accounts_delete ON gateway.provider_accounts
  FOR DELETE
  USING (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

CREATE POLICY provider_accounts_shared_insert ON gateway.provider_accounts
  FOR INSERT
  WITH CHECK (owner_user_id IS NULL AND NOT house AND gateway.may_share());

CREATE POLICY provider_accounts_shared_update ON gateway.provider_accounts
  FOR UPDATE
  USING (owner_user_id IS NULL AND NOT house AND gateway.may_share())
  WITH CHECK (owner_user_id IS NULL AND NOT house AND gateway.may_share());

CREATE POLICY provider_accounts_shared_delete ON gateway.provider_accounts
  FOR DELETE
  USING (owner_user_id IS NULL AND NOT house AND gateway.may_share());

CREATE POLICY provider_models_select ON gateway.provider_models
  FOR SELECT
  USING (EXISTS (SELECT 1 FROM gateway.provider_accounts AS a WHERE a.id = account_id));

CREATE POLICY provider_models_insert ON gateway.provider_models
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND a.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY provider_models_delete ON gateway.provider_models
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND a.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY provider_models_shared_insert ON gateway.provider_models
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id AND a.owner_user_id IS NULL AND NOT a.house AND gateway.may_share()
  ));

CREATE POLICY provider_models_shared_delete ON gateway.provider_models
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id AND a.owner_user_id IS NULL AND NOT a.house AND gateway.may_share()
  ));

CREATE POLICY credentials_own ON gateway.credentials
  FOR ALL
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref
      AND a.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref
      AND a.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY credentials_shared ON gateway.credentials
  FOR ALL
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref AND a.owner_user_id IS NULL AND NOT a.house AND gateway.may_share()
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref AND a.owner_user_id IS NULL AND NOT a.house AND gateway.may_share()
  ));

GRANT SELECT, INSERT, DELETE ON gateway.provider_account_teams TO upmo_app;
