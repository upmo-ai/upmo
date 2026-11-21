-- +goose Up

CREATE TABLE agents.agents (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL DEFAULT 'private',
  team_ids uuid[] NOT NULL DEFAULT '{}',
  installed boolean NOT NULL DEFAULT false,
  name text NOT NULL,
  slug text NOT NULL,
  role text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  instructions text NOT NULL DEFAULT '',
  default_route text,
  default_model_provider text,
  default_model_id text,
  tool_classes text[] NOT NULL DEFAULT '{}',
  connector_ids text[] NOT NULL DEFAULT '{}',
  knowledge_folder_ids uuid[] NOT NULL DEFAULT '{}',
  plan_approval text NOT NULL DEFAULT 'risky',
  check_criteria jsonb NOT NULL DEFAULT '[]'::jsonb,
  enabled boolean NOT NULL DEFAULT true,
  version int NOT NULL DEFAULT 1,
  pack_version text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT agents_visibility_check CHECK (visibility IN ('private', 'team', 'everyone')),
  CONSTRAINT agents_team_ids_check CHECK ((visibility = 'team') = (cardinality(team_ids) > 0)),
  CONSTRAINT agents_name_check CHECK (char_length(name) BETWEEN 1 AND 60),
  CONSTRAINT agents_slug_check CHECK (char_length(slug) BETWEEN 1 AND 80 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
  CONSTRAINT agents_role_check CHECK (char_length(role) <= 120),
  CONSTRAINT agents_description_check CHECK (char_length(description) <= 300),
  CONSTRAINT agents_instructions_check CHECK (char_length(instructions) <= 4000),
  CONSTRAINT agents_route_check CHECK (default_route IS NULL OR char_length(default_route) BETWEEN 1 AND 64),
  CONSTRAINT agents_model_check CHECK (
    (default_model_provider IS NULL AND default_model_id IS NULL)
    OR (default_model_provider IS NOT NULL AND default_model_id IS NOT NULL
        AND char_length(default_model_provider) BETWEEN 1 AND 60
        AND char_length(default_model_id) BETWEEN 1 AND 120)
  ),
  CONSTRAINT agents_plan_approval_check CHECK (plan_approval IN ('always', 'risky', 'never')),
  CONSTRAINT agents_check_criteria_check CHECK (jsonb_typeof(check_criteria) = 'array'),
  CONSTRAINT agents_version_check CHECK (version >= 1)
);

CREATE UNIQUE INDEX agents_own_name ON agents.agents (owner_user_id, lower(name)) WHERE visibility = 'private';
CREATE UNIQUE INDEX agents_shared_name ON agents.agents (lower(name)) WHERE visibility <> 'private';
CREATE UNIQUE INDEX agents_own_slug ON agents.agents (owner_user_id, lower(slug)) WHERE visibility = 'private';
CREATE UNIQUE INDEX agents_shared_slug ON agents.agents (lower(slug)) WHERE visibility <> 'private';
CREATE INDEX agents_team_ids ON agents.agents USING gin (team_ids);

CREATE TABLE agents.versions (
  agent_id uuid NOT NULL REFERENCES agents.agents (id) ON DELETE CASCADE,
  version int NOT NULL,
  digest text NOT NULL,
  snapshot jsonb NOT NULL,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (agent_id, version),
  CONSTRAINT versions_version_check CHECK (version >= 1),
  CONSTRAINT versions_digest_check CHECK (digest ~ '^[0-9a-f]{64}$')
);

CREATE TABLE agents.lessons (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  agent_id uuid NOT NULL REFERENCES agents.agents (id) ON DELETE CASCADE,
  title text NOT NULL,
  cue text NOT NULL,
  body text NOT NULL,
  always_inline boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT lessons_title_check CHECK (title <> ''),
  CONSTRAINT lessons_cue_check CHECK (cue <> ''),
  CONSTRAINT lessons_body_check CHECK (body <> '')
);

CREATE UNIQUE INDEX lessons_agent_title ON agents.lessons (agent_id, title);
CREATE INDEX lessons_agent ON agents.lessons (agent_id);

ALTER TABLE agents.agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE agents.agents FORCE ROW LEVEL SECURITY;
ALTER TABLE agents.versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE agents.versions FORCE ROW LEVEL SECURITY;
ALTER TABLE agents.lessons ENABLE ROW LEVEL SECURITY;
ALTER TABLE agents.lessons FORCE ROW LEVEL SECURITY;

CREATE POLICY agents_owner_all ON agents.agents
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'agents.agents'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'agents.agents'::regclass))
  );

CREATE POLICY versions_owner_all ON agents.versions
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'agents.versions'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'agents.versions'::regclass))
  );

CREATE POLICY lessons_owner_all ON agents.lessons
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'agents.lessons'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'agents.lessons'::regclass))
  );

CREATE POLICY agents_select ON agents.agents
  FOR SELECT
  USING (
    'agent:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND (
      (identity.may_use(owner_user_id, visibility, team_ids) AND (enabled OR visibility = 'private'))
      OR identity.may_manage(owner_user_id, visibility)
    )
  );

CREATE POLICY agents_insert ON agents.agents
  FOR INSERT
  WITH CHECK (
    NOT installed
    AND 'agent:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND identity.may_manage(owner_user_id, visibility)
  );

CREATE POLICY agents_update ON agents.agents
  FOR UPDATE
  USING (
    'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND identity.may_manage(owner_user_id, visibility)
  )
  WITH CHECK (
    'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND identity.may_manage(owner_user_id, visibility)
  );

CREATE POLICY agents_delete ON agents.agents
  FOR DELETE
  USING (
    'agent:delete' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND identity.may_manage(owner_user_id, visibility)
  );

CREATE POLICY versions_select ON agents.versions
  FOR SELECT
  USING (
    EXISTS (
      SELECT 1 FROM agents.agents AS a
      WHERE a.id = agent_id
        AND 'agent:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
        AND (
          (identity.may_use(a.owner_user_id, a.visibility, a.team_ids) AND (a.enabled OR a.visibility = 'private'))
          OR identity.may_manage(a.owner_user_id, a.visibility)
        )
    )
  );

CREATE POLICY versions_insert ON agents.versions
  FOR INSERT
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM agents.agents AS a
      WHERE a.id = agent_id
        AND (
          'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
          OR 'agent:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
        )
        AND identity.may_manage(a.owner_user_id, a.visibility)
    )
  );

CREATE POLICY lessons_select ON agents.lessons
  FOR SELECT
  USING (
    current_setting('upmo.lesson_access', true) = 'admin'
    OR agent_id = NULLIF(current_setting('upmo.agent_id', true), '')::uuid
    OR EXISTS (
      SELECT 1 FROM agents.agents AS a
      WHERE a.id = agent_id
        AND 'agent:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
        AND (
          (identity.may_use(a.owner_user_id, a.visibility, a.team_ids) AND (a.enabled OR a.visibility = 'private'))
          OR identity.may_manage(a.owner_user_id, a.visibility)
        )
    )
  );

CREATE POLICY lessons_insert ON agents.lessons
  FOR INSERT
  WITH CHECK (
    current_setting('upmo.lesson_access', true) = 'admin'
    OR EXISTS (
      SELECT 1 FROM agents.agents AS a
      WHERE a.id = agent_id
        AND (
          'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
          OR 'agent:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
        )
        AND identity.may_manage(a.owner_user_id, a.visibility)
    )
  );

CREATE POLICY lessons_update ON agents.lessons
  FOR UPDATE
  USING (
    current_setting('upmo.lesson_access', true) = 'admin'
    OR EXISTS (
      SELECT 1 FROM agents.agents AS a
      WHERE a.id = agent_id
        AND 'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
        AND identity.may_manage(a.owner_user_id, a.visibility)
    )
  )
  WITH CHECK (
    current_setting('upmo.lesson_access', true) = 'admin'
    OR EXISTS (
      SELECT 1 FROM agents.agents AS a
      WHERE a.id = agent_id
        AND 'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
        AND identity.may_manage(a.owner_user_id, a.visibility)
    )
  );

CREATE POLICY lessons_delete ON agents.lessons
  FOR DELETE
  USING (
    current_setting('upmo.lesson_access', true) = 'admin'
    OR EXISTS (
      SELECT 1 FROM agents.agents AS a
      WHERE a.id = agent_id
        AND 'agent:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
        AND identity.may_manage(a.owner_user_id, a.visibility)
    )
  );

GRANT USAGE ON SCHEMA agents TO upmo_app;
GRANT SELECT, INSERT, DELETE ON agents.agents TO upmo_app;
GRANT UPDATE (
  name, slug, role, description, instructions, default_route,
  default_model_provider, default_model_id, tool_classes, connector_ids,
  knowledge_folder_ids, plan_approval, check_criteria, enabled,
  visibility, team_ids, version, updated_at
) ON agents.agents TO upmo_app;
GRANT SELECT, INSERT ON agents.versions TO upmo_app;
GRANT SELECT, INSERT, DELETE ON agents.lessons TO upmo_app;
GRANT UPDATE (title, cue, body, always_inline, updated_at) ON agents.lessons TO upmo_app;

-- +goose Down

DROP TABLE IF EXISTS agents.lessons;
DROP TABLE IF EXISTS agents.versions;
DROP TABLE IF EXISTS agents.agents;
