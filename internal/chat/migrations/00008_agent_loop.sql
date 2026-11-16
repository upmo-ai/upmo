-- +goose Up

CREATE TABLE chat.turns (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid,
  seq int NOT NULL,
  role text NOT NULL,
  body text NOT NULL DEFAULT '',
  tool_calls jsonb,
  tool_name text NOT NULL DEFAULT '',
  tool_id text NOT NULL DEFAULT '',
  is_error boolean NOT NULL DEFAULT false,
  offloaded boolean NOT NULL DEFAULT false,
  tainted boolean NOT NULL DEFAULT false,
  private_read boolean NOT NULL DEFAULT false,
  model text NOT NULL DEFAULT '',
  input_tokens int NOT NULL DEFAULT 0,
  output_tokens int NOT NULL DEFAULT 0,
  cost_micro_usd bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (chat_id, seq),
  CONSTRAINT chat_turns_seq_check CHECK (seq > 0),
  CONSTRAINT chat_turns_role_check CHECK (role IN ('user', 'assistant', 'tool', 'blocked', 'simulated'))
);

CREATE INDEX chat_turns_chat_idx ON chat.turns (chat_id, seq);

ALTER TABLE chat.turns ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.turns FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_turns_owner ON chat.turns
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.turns'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.turns'::regclass))
  );

CREATE POLICY chat_turns_select ON chat.turns
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY chat_turns_insert ON chat.turns
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

GRANT SELECT, INSERT ON chat.turns TO upmo_app;

CREATE TABLE chat.holds (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid,
  user_id uuid NOT NULL,
  reason text NOT NULL,
  action text NOT NULL,
  preview text NOT NULL DEFAULT '',
  calls jsonb[] NOT NULL DEFAULT '{}'::jsonb[],
  untrusted_origin boolean NOT NULL DEFAULT false,
  state text NOT NULL DEFAULT 'pending',
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  CONSTRAINT holds_reason_check CHECK (reason IN ('ask', 'expired')),
  CONSTRAINT holds_action_check CHECK (char_length(action) BETWEEN 1 AND 200),
  CONSTRAINT holds_state_check CHECK (state IN ('pending', 'approved', 'denied', 'closed')),
  CONSTRAINT holds_decided_check CHECK ((state = 'pending') = (decided_at IS NULL))
);

CREATE UNIQUE INDEX holds_one_pending
  ON chat.holds (chat_id)
  WHERE state = 'pending';

CREATE INDEX holds_user_idx ON chat.holds (user_id, created_at DESC);

ALTER TABLE chat.holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.holds FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_holds_owner ON chat.holds
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.holds'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.holds'::regclass))
  );

CREATE POLICY chat_holds_select ON chat.holds
  FOR SELECT
  USING (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY chat_holds_insert ON chat.holds
  FOR INSERT
  WITH CHECK (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  );

CREATE POLICY chat_holds_update ON chat.holds
  FOR UPDATE
  USING (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  )
  WITH CHECK (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

GRANT SELECT, INSERT ON chat.holds TO upmo_app;
GRANT UPDATE (state, decided_at) ON chat.holds TO upmo_app;

CREATE TABLE chat.action_logs (
  scope_id uuid NOT NULL,
  call_id text NOT NULL,
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid,
  user_id uuid NOT NULL,
  tool_name text NOT NULL,
  action text NOT NULL DEFAULT '',
  preview text NOT NULL DEFAULT '',
  state text NOT NULL,
  untrusted_origin boolean NOT NULL DEFAULT false,
  dry_run boolean NOT NULL DEFAULT false,
  response_digest text NOT NULL DEFAULT '',
  error_text text NOT NULL DEFAULT '',
  executed_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (scope_id, call_id),
  CONSTRAINT chat_action_logs_call_id_check CHECK (char_length(call_id) BETWEEN 1 AND 200),
  CONSTRAINT chat_action_logs_state_check CHECK (state IN ('dialing', 'done', 'failed', 'simulated'))
);

CREATE INDEX chat_action_logs_chat_idx ON chat.action_logs (chat_id, executed_at DESC);

ALTER TABLE chat.action_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.action_logs FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_action_logs_owner ON chat.action_logs
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.action_logs'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.action_logs'::regclass))
  );

CREATE POLICY chat_action_logs_select ON chat.action_logs
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY chat_action_logs_insert ON chat.action_logs
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY chat_action_logs_update ON chat.action_logs
  FOR UPDATE
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

GRANT SELECT, INSERT ON chat.action_logs TO upmo_app;
GRANT UPDATE (state, response_digest, error_text, completed_at) ON chat.action_logs TO upmo_app;

CREATE TABLE chat.findings (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid,
  kind text NOT NULL DEFAULT 'output',
  title text NOT NULL DEFAULT '',
  body text NOT NULL,
  source_ref text NOT NULL DEFAULT '',
  untrusted boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT chat_findings_kind_check CHECK (kind IN ('finding', 'output', 'tool_output'))
);

CREATE INDEX chat_findings_chat_idx ON chat.findings (chat_id, created_at);

ALTER TABLE chat.findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.findings FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_findings_owner ON chat.findings
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.findings'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.findings'::regclass))
  );

CREATE POLICY chat_findings_select ON chat.findings
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY chat_findings_insert ON chat.findings
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

GRANT SELECT, INSERT ON chat.findings TO upmo_app;

-- +goose Down

DROP TABLE IF EXISTS chat.findings;
DROP TABLE IF EXISTS chat.action_logs;
DROP TABLE IF EXISTS chat.holds;
DROP TABLE IF EXISTS chat.turns;
