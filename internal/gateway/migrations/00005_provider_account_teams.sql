-- +goose Up

-- A provider account is personal (it has an owner), or shared: created by an
-- install admin (teams:manage) and either for the whole workspace, which is
-- what a global account always was, or for specific teams. A team share is a
-- row here; a personal or a workspace account has none.
--
-- This migration needs the identity schema (teams and their members). brain
-- migrates identity before gateway, and cmd/gateway does the same.

CREATE TABLE gateway.provider_account_teams (
  account_id uuid NOT NULL REFERENCES gateway.provider_accounts (id) ON DELETE CASCADE,
  team_id uuid NOT NULL REFERENCES identity.teams (id) ON DELETE CASCADE,
  PRIMARY KEY (account_id, team_id)
);

CREATE INDEX provider_account_teams_team ON gateway.provider_account_teams (team_id);

-- The team through which a shared account was reached, kept as it was when
-- the attempt ran. It has no foreign key on purpose: attribution must survive
-- a later change to the team or its shares. Null for a personal or workspace
-- account.
ALTER TABLE gateway.ledger ADD COLUMN owner_team_id uuid;

-- A credential ref is unique across every account that has its own key, and a
-- shared account has one. Only the house accounts, which read their key from
-- the environment under a shared ref, are left out.
DROP INDEX gateway.provider_accounts_own_ref;
CREATE UNIQUE INDEX provider_accounts_ref
  ON gateway.provider_accounts (credential_ref)
  WHERE NOT house;

-- True when the request holds teams:manage. The app writes the permission
-- into upmo.allow for the transaction, as it does for every other module.
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

-- The teams the request's person is on. It runs as its owner so that the
-- policies below do not need the caller to read identity tables.
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

-- Whether an account is shared with any team at all. It runs as its owner so
-- that it sees every share row, not only the caller's: an account shared with
-- other teams must not read as a workspace account to someone outside them.
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

-- A person sees the share rows of their own teams. An admin sees them all.
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

-- Who sees an account. Their own. A workspace account, which has no team rows.
-- A team account, when they are on one of its teams. An admin sees every
-- shared account, to manage it. Nobody else sees a team account at all.
DROP POLICY provider_accounts_select ON gateway.provider_accounts;
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

-- Only an admin writes a shared account. House accounts stay with the owner
-- role: they are read from the environment, not created here.
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

-- A model list follows its account for reading, so a team member sees the
-- models of a shared account. Only an admin writes them.
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

-- A sealed key stays out of reach of a team member and of a workspace member:
-- there is no policy for them. Only the owner of a personal account, and an
-- admin for a shared one, reach it (the admin to replace it or to test it).
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

-- +goose Down

DROP POLICY IF EXISTS credentials_shared ON gateway.credentials;
DROP POLICY IF EXISTS provider_models_shared_delete ON gateway.provider_models;
DROP POLICY IF EXISTS provider_models_shared_insert ON gateway.provider_models;
DROP POLICY IF EXISTS provider_accounts_shared_delete ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_shared_update ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_shared_insert ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_select ON gateway.provider_accounts;
CREATE POLICY provider_accounts_select ON gateway.provider_accounts
  FOR SELECT
  USING (
    owner_user_id IS NULL
    OR owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  );

-- A team account has no place in the old shape, where every account without
-- an owner is visible to everyone. It goes, with its routes' links, models and
-- key, rather than turning into a workspace account.
DELETE FROM gateway.route_links
WHERE provider_account_id IN (SELECT account_id FROM gateway.provider_account_teams);
DELETE FROM gateway.credentials
WHERE ref IN (
  SELECT a.credential_ref
  FROM gateway.provider_accounts AS a
  JOIN gateway.provider_account_teams AS t ON t.account_id = a.id
);
DELETE FROM gateway.provider_accounts
WHERE id IN (SELECT account_id FROM gateway.provider_account_teams);

DROP TABLE IF EXISTS gateway.provider_account_teams;
DROP FUNCTION IF EXISTS gateway.team_shared(uuid);
DROP FUNCTION IF EXISTS gateway.my_teams();
DROP FUNCTION IF EXISTS gateway.may_share();

DROP INDEX IF EXISTS gateway.provider_accounts_ref;
CREATE UNIQUE INDEX provider_accounts_own_ref
  ON gateway.provider_accounts (credential_ref)
  WHERE owner_user_id IS NOT NULL;

ALTER TABLE gateway.ledger DROP COLUMN owner_team_id;
