-- +goose Up

ALTER TABLE chat.agents
  ADD COLUMN visibility text NOT NULL DEFAULT 'private',
  ADD COLUMN team_ids uuid[] NOT NULL DEFAULT '{}';

-- Agents with no owner were global. Their creator was never recorded.
UPDATE chat.agents
SET visibility = 'everyone'
WHERE owner_user_id IS NULL;

-- +goose StatementBegin
DO $$
DECLARE
  fallback uuid;
BEGIN
  IF EXISTS (SELECT 1 FROM chat.agents WHERE owner_user_id IS NULL) THEN
    SELECT person.id INTO fallback
    FROM identity.users AS person
    JOIN identity.roles AS role ON role.id = person.role_id
    WHERE role.key = 'owner'
      AND person.joined_at IS NOT NULL
      AND person.disabled_at IS NULL
    ORDER BY person.joined_at, person.id
    LIMIT 1;
    IF fallback IS NULL THEN
      RAISE EXCEPTION 'chat agents need a joined owner';
    END IF;
    UPDATE chat.agents
    SET owner_user_id = fallback
    WHERE owner_user_id IS NULL;
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE chat.agents
  ALTER COLUMN owner_user_id SET NOT NULL;

ALTER TABLE chat.agents
  ADD CONSTRAINT agents_visibility_check CHECK (visibility IN ('private', 'team', 'everyone')),
  ADD CONSTRAINT agents_team_ids_check CHECK ((visibility = 'team') = (cardinality(team_ids) > 0));

DROP INDEX chat.agents_own_name;
DROP INDEX chat.agents_global_name;

CREATE UNIQUE INDEX agents_own_name ON chat.agents (owner_user_id, lower(name)) WHERE visibility = 'private';
CREATE UNIQUE INDEX agents_shared_name ON chat.agents (lower(name)) WHERE visibility <> 'private';

CREATE INDEX agents_team_ids ON chat.agents USING gin (team_ids);

DROP POLICY agents_select ON chat.agents;
DROP POLICY agents_insert ON chat.agents;
DROP POLICY agents_update ON chat.agents;
DROP POLICY agents_delete ON chat.agents;

CREATE POLICY agents_select ON chat.agents
  FOR SELECT
  USING (
    'agent:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND (
      (identity.may_use(owner_user_id, visibility, team_ids) AND (enabled OR visibility = 'private'))
      OR identity.may_manage(owner_user_id, visibility)
    )
  );

CREATE POLICY agents_insert ON chat.agents
  FOR INSERT
  WITH CHECK (
    'agent:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND identity.may_manage(owner_user_id, visibility)
  );

CREATE POLICY agents_update ON chat.agents
  FOR UPDATE
  USING (
    'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND identity.may_manage(owner_user_id, visibility)
  )
  WITH CHECK (
    'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND identity.may_manage(owner_user_id, visibility)
  );

CREATE POLICY agents_delete ON chat.agents
  FOR DELETE
  USING (
    'agent:delete' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND identity.may_manage(owner_user_id, visibility)
  );

GRANT UPDATE (visibility, team_ids) ON chat.agents TO upmo_app;

-- +goose Down

DROP POLICY IF EXISTS agents_delete ON chat.agents;
DROP POLICY IF EXISTS agents_update ON chat.agents;
DROP POLICY IF EXISTS agents_insert ON chat.agents;
DROP POLICY IF EXISTS agents_select ON chat.agents;

-- Team visibility has no place among the old global rules; remove those rows.
DELETE FROM chat.agents WHERE visibility = 'team';

ALTER TABLE chat.agents
  ALTER COLUMN owner_user_id DROP NOT NULL;

UPDATE chat.agents
SET owner_user_id = NULL
WHERE visibility <> 'private';

DROP INDEX chat.agents_team_ids;
DROP INDEX chat.agents_shared_name;
DROP INDEX chat.agents_own_name;

CREATE UNIQUE INDEX agents_own_name ON chat.agents (owner_user_id, lower(name)) WHERE owner_user_id IS NOT NULL;
CREATE UNIQUE INDEX agents_global_name ON chat.agents (lower(name)) WHERE owner_user_id IS NULL;

ALTER TABLE chat.agents
  DROP CONSTRAINT agents_team_ids_check,
  DROP CONSTRAINT agents_visibility_check;

ALTER TABLE chat.agents
  DROP COLUMN team_ids,
  DROP COLUMN visibility;

CREATE POLICY agents_select ON chat.agents
  FOR SELECT
  USING (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    OR (NULLIF(current_setting('upmo.user_id', true), '')::uuid IS NOT NULL AND owner_user_id IS NULL AND enabled)
    OR (NULLIF(current_setting('upmo.user_id', true), '')::uuid IS NOT NULL AND owner_user_id IS NULL
        AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  );

CREATE POLICY agents_insert ON chat.agents
  FOR INSERT
  WITH CHECK (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    OR (owner_user_id IS NULL AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  );

CREATE POLICY agents_update ON chat.agents
  FOR UPDATE
  USING (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    OR (owner_user_id IS NULL AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  )
  WITH CHECK (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    OR (owner_user_id IS NULL AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  );

CREATE POLICY agents_delete ON chat.agents
  FOR DELETE
  USING (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    OR (owner_user_id IS NULL AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  );
