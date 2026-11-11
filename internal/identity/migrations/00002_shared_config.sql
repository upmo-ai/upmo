-- Shared access rules for config objects (providers, connectors, routes).
-- The gateway, which resolves keys outside row security, and the row policies
-- of the tables that hold these objects call the same pure rule, so they
-- cannot drift.

-- +goose Up

-- Pure rule. Both callers use it, so they cannot drift.
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

-- For row policies: the caller comes from the request settings.
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

-- For the gateway process, which resolves keys outside row security.
-- +goose StatementBegin
CREATE FUNCTION identity.teams_of(person uuid) RETURNS uuid[]
LANGUAGE sql STABLE SET search_path = pg_catalog
AS $$
  SELECT COALESCE(array_agg(team_id), '{}'::uuid[])
  FROM identity.team_members WHERE user_id = person;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.can_use(uuid, uuid[], uuid, text, uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.can_use(uuid, uuid[], uuid, text, uuid[]) TO upmo_app;

REVOKE ALL ON FUNCTION identity.may_use(uuid, text, uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.may_use(uuid, text, uuid[]) TO upmo_app;

REVOKE ALL ON FUNCTION identity.may_manage(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.may_manage(uuid, text) TO upmo_app;

-- teams_of reads identity.team_members for the gateway process, which runs as
-- the table owner, not as upmo_app.
REVOKE ALL ON FUNCTION identity.teams_of(uuid) FROM PUBLIC, upmo_app;

-- +goose Down

DROP FUNCTION IF EXISTS identity.teams_of(uuid);
DROP FUNCTION IF EXISTS identity.may_manage(uuid, text);
DROP FUNCTION IF EXISTS identity.may_use(uuid, text, uuid[]);
DROP FUNCTION IF EXISTS identity.can_use(uuid, uuid[], uuid, text, uuid[]);
