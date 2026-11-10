-- +goose Up

-- An agent is a saved profile a person picks in the composer: a name, some
-- instructions added to the system text, and optionally a route and a model
-- (by provider name and model id, not an account) it defaults to. A row with
-- no owner is a global agent that the whole workspace can use. A name is
-- unique per owner, and among the global agents, without regard to case.
CREATE TABLE chat.agents (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  owner_user_id uuid,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  prompt_overlay text NOT NULL DEFAULT '',
  route text,
  model_provider text,
  model_id text,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT agents_name_check CHECK (char_length(name) BETWEEN 1 AND 60),
  CONSTRAINT agents_description_check CHECK (char_length(description) <= 300),
  CONSTRAINT agents_overlay_check CHECK (char_length(prompt_overlay) <= 4000),
  CONSTRAINT agents_route_check CHECK (route IS NULL OR char_length(route) BETWEEN 1 AND 64),
  CONSTRAINT agents_model_check CHECK (
    (model_provider IS NULL AND model_id IS NULL)
    OR (model_provider IS NOT NULL AND model_id IS NOT NULL
        AND char_length(model_provider) BETWEEN 1 AND 60
        AND char_length(model_id) BETWEEN 1 AND 120)
  )
);

CREATE UNIQUE INDEX agents_own_name ON chat.agents (owner_user_id, lower(name)) WHERE owner_user_id IS NOT NULL;
CREATE UNIQUE INDEX agents_global_name ON chat.agents (lower(name)) WHERE owner_user_id IS NULL;

ALTER TABLE chat.agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.agents FORCE ROW LEVEL SECURITY;

-- Everyone signed in (a session with no person sees nothing) sees their own agents and the enabled global ones. A
-- person writes their own. The global ones, enabled or not, are seen and
-- written only by whoever holds settings:manage.
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

GRANT SELECT, INSERT, DELETE ON chat.agents TO upmo_app;
GRANT UPDATE (name, description, prompt_overlay, route, model_provider, model_id, enabled, updated_at) ON chat.agents TO upmo_app;

-- +goose Down

DROP TABLE IF EXISTS chat.agents;
