-- +goose Up

ALTER TABLE tasks.tasks
  ADD COLUMN budget_micro_usd bigint NOT NULL DEFAULT 0,
  ADD COLUMN held_micro_usd bigint NOT NULL DEFAULT 0,
  ADD COLUMN spent_micro_usd bigint NOT NULL DEFAULT 0,
  ADD CONSTRAINT tasks_budget_micro_check CHECK (budget_micro_usd >= 0),
  ADD CONSTRAINT tasks_held_micro_check CHECK (held_micro_usd BETWEEN 0 AND budget_micro_usd),
  ADD CONSTRAINT tasks_spent_micro_check CHECK (spent_micro_usd >= 0),
  ADD CONSTRAINT tasks_budget_single_unit_check CHECK (NOT (budget > 0 AND budget_micro_usd > 0));

GRANT UPDATE (budget_micro_usd, held_micro_usd, spent_micro_usd) ON tasks.tasks TO upmo_app;

ALTER TABLE tasks.approvals
  ADD COLUMN preview text NOT NULL DEFAULT '',
  ADD COLUMN calls jsonb[] NOT NULL DEFAULT '{}'::jsonb[],
  ADD COLUMN untrusted_origin boolean NOT NULL DEFAULT false;

CREATE TABLE tasks.findings (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id) ON DELETE CASCADE,
  run_id uuid REFERENCES tasks.runs (id) ON DELETE CASCADE,
  kind text NOT NULL DEFAULT 'finding',
  title text NOT NULL DEFAULT '',
  body text NOT NULL,
  source_ref text NOT NULL DEFAULT '',
  untrusted boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT findings_kind_check CHECK (kind IN ('finding', 'output', 'tool_output'))
);

CREATE INDEX findings_task_idx ON tasks.findings (task_id, created_at);

ALTER TABLE tasks.findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.findings FORCE ROW LEVEL SECURITY;

CREATE POLICY findings_owner ON tasks.findings
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.findings'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.findings'::regclass))
  );

CREATE POLICY findings_select ON tasks.findings
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY findings_insert ON tasks.findings
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

GRANT SELECT, INSERT ON tasks.findings TO upmo_app;

CREATE TABLE tasks.turns (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id) ON DELETE CASCADE,
  run_id uuid NOT NULL REFERENCES tasks.runs (id) ON DELETE CASCADE,
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
  UNIQUE (run_id, seq),
  CONSTRAINT turns_seq_check CHECK (seq > 0),
  CONSTRAINT turns_role_check CHECK (role IN ('user', 'assistant', 'tool', 'blocked', 'simulated'))
);

CREATE INDEX turns_task_idx ON tasks.turns (task_id, seq);

ALTER TABLE tasks.turns ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.turns FORCE ROW LEVEL SECURITY;

CREATE POLICY turns_owner ON tasks.turns
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.turns'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.turns'::regclass))
  );

CREATE POLICY turns_select ON tasks.turns
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY turns_insert ON tasks.turns
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

GRANT SELECT, INSERT ON tasks.turns TO upmo_app;

CREATE TABLE tasks.action_logs (
  scope_id uuid NOT NULL,
  call_id text NOT NULL,
  task_id uuid NOT NULL REFERENCES tasks.tasks (id) ON DELETE CASCADE,
  run_id uuid REFERENCES tasks.runs (id) ON DELETE CASCADE,
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
  CONSTRAINT action_logs_call_id_check CHECK (char_length(call_id) BETWEEN 1 AND 200),
  CONSTRAINT action_logs_state_check CHECK (state IN ('dialing', 'done', 'failed', 'simulated'))
);

CREATE INDEX action_logs_task_idx ON tasks.action_logs (task_id, executed_at DESC);

ALTER TABLE tasks.action_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.action_logs FORCE ROW LEVEL SECURITY;

CREATE POLICY action_logs_owner ON tasks.action_logs
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.action_logs'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.action_logs'::regclass))
  );

CREATE POLICY action_logs_select ON tasks.action_logs
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY action_logs_insert ON tasks.action_logs
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY action_logs_update ON tasks.action_logs
  FOR UPDATE
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

GRANT SELECT, INSERT ON tasks.action_logs TO upmo_app;
GRANT UPDATE (state, response_digest, error_text, completed_at) ON tasks.action_logs TO upmo_app;

-- +goose Down

DROP TABLE IF EXISTS tasks.action_logs;
DROP TABLE IF EXISTS tasks.turns;
DROP TABLE IF EXISTS tasks.findings;

ALTER TABLE tasks.approvals
  DROP COLUMN IF EXISTS untrusted_origin,
  DROP COLUMN IF EXISTS calls,
  DROP COLUMN IF EXISTS preview;

REVOKE UPDATE (budget_micro_usd, held_micro_usd, spent_micro_usd) ON tasks.tasks FROM upmo_app;

ALTER TABLE tasks.tasks
  DROP CONSTRAINT IF EXISTS tasks_budget_single_unit_check,
  DROP CONSTRAINT IF EXISTS tasks_spent_micro_check,
  DROP CONSTRAINT IF EXISTS tasks_held_micro_check,
  DROP CONSTRAINT IF EXISTS tasks_budget_micro_check,
  DROP COLUMN IF EXISTS spent_micro_usd,
  DROP COLUMN IF EXISTS held_micro_usd,
  DROP COLUMN IF EXISTS budget_micro_usd;
