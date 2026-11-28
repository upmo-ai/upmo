-- +goose Up

CREATE TABLE tasks.templates (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  owner_user_id uuid NOT NULL,
  goal text NOT NULL,
  agent_id uuid NOT NULL,
  budget bigint NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT templates_goal_check CHECK (char_length(goal) BETWEEN 1 AND 200),
  CONSTRAINT templates_budget_check CHECK (budget BETWEEN 1 AND 1000000000)
);

CREATE TABLE tasks.template_steps (
  template_id uuid NOT NULL REFERENCES tasks.templates (id) ON DELETE CASCADE,
  seq int NOT NULL,
  title text NOT NULL,
  PRIMARY KEY (template_id, seq),
  CONSTRAINT template_steps_seq_check CHECK (seq > 0),
  CONSTRAINT template_steps_title_check CHECK (char_length(title) BETWEEN 1 AND 200)
);

CREATE TABLE tasks.template_deliveries (
  template_id uuid NOT NULL REFERENCES tasks.templates (id) ON DELETE CASCADE,
  seq int NOT NULL,
  target text NOT NULL,
  PRIMARY KEY (template_id, seq),
  CONSTRAINT template_deliveries_seq_check CHECK (seq > 0),
  CONSTRAINT template_deliveries_target_check CHECK (char_length(target) BETWEEN 1 AND 200)
);

CREATE TABLE tasks.tasks (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL DEFAULT 'task',
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL DEFAULT 'private',
  visibility_team_id uuid,
  goal text NOT NULL,
  status text NOT NULL,
  parent_id uuid REFERENCES tasks.tasks (id),
  budget bigint NOT NULL DEFAULT 0,
  held bigint NOT NULL DEFAULT 0,
  reserved bigint NOT NULL DEFAULT 0,
  spent bigint NOT NULL DEFAULT 0,
  paused boolean NOT NULL DEFAULT false,
  allow text NOT NULL DEFAULT '',
  tainted boolean NOT NULL DEFAULT false,
  private_read boolean NOT NULL DEFAULT false,
  runner_owned boolean NOT NULL DEFAULT false,
  claim_token uuid,
  lease_until timestamptz,
  cause text NOT NULL DEFAULT '',
  result jsonb,
  worktree text NOT NULL DEFAULT '',
  automation_id uuid,
  passes boolean NOT NULL DEFAULT false,
  agent_id uuid,
  agent_version int,
  template_id uuid REFERENCES tasks.templates (id) ON DELETE SET NULL,
  source_chat_id uuid,
  up_to_line_id uuid,
  dry_run boolean NOT NULL DEFAULT false,
  flow text NOT NULL DEFAULT 'standard',
  plan_approval text NOT NULL DEFAULT 'risky',
  plan jsonb,
  plan_digest text NOT NULL DEFAULT '',
  stalls int NOT NULL DEFAULT 0,
  failures int NOT NULL DEFAULT 0,
  replanned boolean NOT NULL DEFAULT false,
  last_gap text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT tasks_type_check CHECK (object_type = 'task'),
  CONSTRAINT tasks_goal_check CHECK (char_length(goal) BETWEEN 1 AND 200),
  CONSTRAINT tasks_status_check CHECK (status IN ('Preparing', 'Planning', 'Working', 'Verifying', 'Needs approval', 'Done', 'Failed')),
  CONSTRAINT tasks_visibility_check CHECK (
    (visibility IN ('private', 'everyone') AND visibility_team_id IS NULL)
    OR (visibility = 'team' AND visibility_team_id IS NOT NULL)
  ),
  CONSTRAINT tasks_parent_check CHECK (parent_id IS NULL OR parent_id <> id),
  CONSTRAINT tasks_budget_check CHECK (budget BETWEEN 0 AND 1000000000),
  CONSTRAINT tasks_held_check CHECK (held BETWEEN 0 AND budget),
  CONSTRAINT tasks_reserved_check CHECK (reserved >= 0),
  CONSTRAINT tasks_spent_check CHECK (spent >= 0),
  CONSTRAINT tasks_allow_check CHECK (char_length(allow) <= 400),
  CONSTRAINT tasks_cause_check CHECK (
    cause = '' OR (
      status = 'Failed'
      AND cause IN ('parent_canceled', 'canceled', 'access', 'stalled', 'failing', 'review_limit', 'unknown')
    )
  ),
  CONSTRAINT tasks_worktree_check CHECK (char_length(worktree) <= 80),
  CONSTRAINT tasks_agent_version_check CHECK (agent_version IS NULL OR agent_version >= 1),
  CONSTRAINT tasks_flow_check CHECK (flow IN ('light', 'standard', 'full')),
  CONSTRAINT tasks_plan_approval_check CHECK (plan_approval IN ('always', 'risky', 'never')),
  CONSTRAINT tasks_plan_check CHECK (plan IS NULL OR jsonb_typeof(plan) = 'object'),
  CONSTRAINT tasks_plan_digest_check CHECK (plan_digest = '' OR plan_digest ~ '^[0-9a-f]{64}$'),
  CONSTRAINT tasks_stalls_check CHECK (stalls >= 0),
  CONSTRAINT tasks_failures_check CHECK (failures >= 0),
  CONSTRAINT tasks_last_gap_check CHECK (char_length(last_gap) <= 4000)
);

CREATE INDEX tasks_parent_idx ON tasks.tasks (parent_id);

CREATE UNIQUE INDEX tasks_worktree_writer
  ON tasks.tasks (worktree)
  WHERE worktree <> '' AND status NOT IN ('Done', 'Failed');

CREATE TABLE tasks.task_events (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id),
  seq int NOT NULL,
  from_status text NOT NULL,
  to_status text NOT NULL,
  input text NOT NULL,
  actor text NOT NULL,
  at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (task_id, seq),
  CONSTRAINT task_events_seq_check CHECK (seq > 0),
  CONSTRAINT task_events_actor_check CHECK (actor IN ('person', 'planner', 'worker', 'verifier', 'core')),
  CONSTRAINT task_events_edge_check CHECK (
    (input = 'open' AND from_status = '' AND to_status IN ('Preparing', 'Planning', 'Working') AND seq = 1)
    OR (input = 'advance' AND (
      (from_status = 'Preparing' AND to_status = 'Working')
      OR (from_status = 'Planning' AND to_status = 'Working')
      OR (from_status = 'Working' AND to_status = 'Verifying')
    ))
    OR (input = 'plan' AND from_status = 'Preparing' AND to_status = 'Planning')
    OR (input = 'ask_plan' AND from_status = 'Planning' AND to_status = 'Needs approval')
    OR (input = 'rework' AND from_status = 'Verifying' AND to_status = 'Working')
    OR (input = 'replan' AND from_status = 'Working' AND to_status = 'Planning')
    OR (input = 'pause' AND from_status = 'Working' AND to_status = 'Needs approval')
    OR (input = 'hold' AND from_status IN ('Working', 'Verifying') AND to_status = 'Needs approval')
    OR (input = 'resume' AND from_status = 'Needs approval' AND to_status = 'Working')
    OR (input = 'approve' AND from_status = 'Needs approval' AND to_status IN ('Working', 'Verifying'))
    OR (input = 'deny' AND from_status = 'Needs approval' AND to_status IN ('Working', 'Planning', 'Failed'))
    OR (input = 'fail' AND to_status = 'Failed' AND from_status IN ('Preparing', 'Planning', 'Working', 'Verifying', 'Needs approval'))
    OR (input = 'pass' AND from_status = 'Verifying' AND to_status = 'Done')
  )
);

CREATE TABLE tasks.chat_events (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id),
  seq int NOT NULL,
  body text NOT NULL,
  at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (task_id, seq),
  CONSTRAINT chat_events_seq_check CHECK (seq > 0),
  CONSTRAINT chat_events_body_check CHECK (char_length(body) BETWEEN 1 AND 4000)
);

CREATE TABLE tasks.checks (
  task_id uuid NOT NULL REFERENCES tasks.tasks (id),
  batch int NOT NULL,
  seq int NOT NULL,
  name text NOT NULL,
  code int NOT NULL,
  unit int NOT NULL DEFAULT 0,
  criterion int NOT NULL DEFAULT 0,
  kind text NOT NULL DEFAULT 'command',
  verdict text NOT NULL DEFAULT 'pass',
  digest text NOT NULL DEFAULT '',
  excerpt text NOT NULL DEFAULT '',
  regressed boolean NOT NULL DEFAULT false,
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (task_id, batch, seq),
  CONSTRAINT checks_batch_check CHECK (batch > 0),
  CONSTRAINT checks_seq_check CHECK (seq > 0),
  CONSTRAINT checks_name_check CHECK (char_length(name) BETWEEN 1 AND 80),
  CONSTRAINT checks_unit_check CHECK (unit >= 0),
  CONSTRAINT checks_criterion_check CHECK (criterion >= 0),
  CONSTRAINT checks_unit_criterion_pair_check CHECK ((unit = 0) = (criterion = 0)),
  CONSTRAINT checks_kind_check CHECK (kind IN ('exists', 'command', 'citation', 'rule')),
  CONSTRAINT checks_verdict_check CHECK (verdict IN ('pass', 'fail', 'unknown')),
  CONSTRAINT checks_excerpt_check CHECK (char_length(excerpt) <= 4000)
);

CREATE TABLE tasks.review_findings (
  task_id uuid NOT NULL REFERENCES tasks.tasks (id) ON DELETE CASCADE,
  seq int NOT NULL,
  unit int NOT NULL,
  criterion int NOT NULL,
  title text NOT NULL,
  detail text NOT NULL DEFAULT '',
  evidence text NOT NULL DEFAULT '',
  blocking boolean NOT NULL DEFAULT true,
  state text NOT NULL DEFAULT 'open',
  round int NOT NULL DEFAULT 1,
  settled_by uuid,
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (task_id, seq),
  CONSTRAINT review_findings_seq_check CHECK (seq > 0),
  CONSTRAINT review_findings_unit_check CHECK (unit BETWEEN 1 AND 12),
  CONSTRAINT review_findings_criterion_check CHECK (criterion BETWEEN 1 AND 6),
  CONSTRAINT review_findings_title_check CHECK (char_length(title) BETWEEN 1 AND 200),
  CONSTRAINT review_findings_detail_check CHECK (char_length(detail) <= 4000),
  CONSTRAINT review_findings_evidence_check CHECK (char_length(evidence) <= 4000),
  CONSTRAINT review_findings_state_check CHECK (state IN ('open', 'fixed', 'accepted', 'dismissed')),
  CONSTRAINT review_findings_round_check CHECK (round > 0),
  CONSTRAINT review_findings_settled_check CHECK ((state = 'accepted') = (settled_by IS NOT NULL))
);

ALTER TABLE tasks.review_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.review_findings FORCE ROW LEVEL SECURITY;

CREATE POLICY review_findings_select ON tasks.review_findings
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY review_findings_owner_insert ON tasks.review_findings
  FOR INSERT
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.review_findings'::regclass))
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

CREATE POLICY review_findings_owner_update ON tasks.review_findings
  FOR UPDATE
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.review_findings'::regclass))
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.review_findings'::regclass))
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

GRANT SELECT ON tasks.review_findings TO upmo_app;
REVOKE INSERT, UPDATE, DELETE ON tasks.review_findings FROM upmo_app;

CREATE TABLE tasks.runs (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id),
  state text NOT NULL,
  turn_seq integer NOT NULL DEFAULT 0,
  msg_seq integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT runs_state_check CHECK (state IN ('queued', 'admitted', 'ended')),
  CONSTRAINT runs_turn_seq_check CHECK (turn_seq >= 0),
  CONSTRAINT runs_msg_seq_check CHECK (msg_seq >= 0)
);

CREATE TABLE tasks.run_events (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  run_id uuid NOT NULL REFERENCES tasks.runs (id),
  seq int NOT NULL,
  from_state text NOT NULL,
  to_state text NOT NULL,
  input text NOT NULL,
  at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, seq),
  CONSTRAINT run_events_seq_check CHECK (seq > 0),
  CONSTRAINT run_events_edge_check CHECK (
    (input = 'admit' AND from_state = '' AND to_state IN ('queued', 'admitted') AND seq = 1)
    OR (input = 'admit' AND from_state = 'queued' AND to_state = 'admitted')
    OR (input = 'end' AND from_state IN ('queued', 'admitted') AND to_state = 'ended')
  )
);

ALTER TABLE tasks.task_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.task_events FORCE ROW LEVEL SECURITY;
ALTER TABLE tasks.chat_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.chat_events FORCE ROW LEVEL SECURITY;
ALTER TABLE tasks.checks ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.checks FORCE ROW LEVEL SECURITY;
ALTER TABLE tasks.runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.runs FORCE ROW LEVEL SECURITY;
ALTER TABLE tasks.run_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.run_events FORCE ROW LEVEL SECURITY;

CREATE POLICY task_events_select ON tasks.task_events
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY task_events_insert ON tasks.task_events
  FOR INSERT
  WITH CHECK (
    actor = 'person'
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
    )
  );

CREATE POLICY chat_events_select ON tasks.chat_events
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY chat_events_insert ON tasks.chat_events
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY checks_select ON tasks.checks
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY checks_owner_insert ON tasks.checks
  FOR INSERT
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.checks'::regclass))
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

CREATE POLICY runs_select ON tasks.runs
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY runs_insert ON tasks.runs
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY runs_update ON tasks.runs
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

CREATE POLICY run_events_select ON tasks.run_events
  FOR SELECT
  USING (EXISTS (
    SELECT 1
    FROM tasks.runs AS run
    JOIN tasks.tasks AS task ON task.id = run.task_id
    WHERE run.id = run_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY run_events_insert ON tasks.run_events
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1
    FROM tasks.runs AS run
    JOIN tasks.tasks AS task ON task.id = run.task_id
    WHERE run.id = run_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

-- +goose StatementBegin
CREATE FUNCTION tasks.cap_run()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
  run_owner uuid;
  admitted_count int;
BEGIN
  IF NEW.state IS DISTINCT FROM 'admitted' THEN
    RETURN NEW;
  END IF;
  SELECT task.owner_user_id
    INTO run_owner
  FROM tasks.tasks AS task
  WHERE task.id = NEW.task_id;
  SELECT count(*)
    INTO admitted_count
  FROM tasks.runs AS run
  JOIN tasks.tasks AS task ON task.id = run.task_id
  WHERE task.owner_user_id = run_owner
    AND run.state = 'admitted'
    AND run.id <> NEW.id;
  IF admitted_count >= 2 THEN
    RAISE EXCEPTION 'tasks: cap';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION tasks.run_born()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.state NOT IN ('queued', 'admitted') THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM tasks.run_events AS event
    WHERE event.run_id = NEW.id
      AND event.input = 'admit'
      AND event.from_state = ''
      AND event.to_state = NEW.state
      AND event.seq = 1
  ) THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION tasks.run_follow()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
  prev text;
  nxt text;
BEGIN
  IF NEW.state IS NOT DISTINCT FROM OLD.state THEN
    RETURN NEW;
  END IF;
  SELECT event.from_state, event.to_state
    INTO prev, nxt
  FROM tasks.run_events AS event
  WHERE event.run_id = NEW.id
  ORDER BY event.seq DESC
  LIMIT 1;
  IF prev IS DISTINCT FROM OLD.state OR nxt IS DISTINCT FROM NEW.state THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION tasks.born()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.status NOT IN ('Preparing', 'Planning', 'Working') THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM tasks.task_events AS event
    WHERE event.task_id = NEW.id
      AND event.input = 'open'
      AND event.from_status = ''
      AND event.to_status = NEW.status
      AND event.seq = 1
  ) THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION tasks.follow()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
  prev text;
  nxt text;
  latest_batch int;
  unit_rec record;
  crit_rec record;
  unit_num int;
  crit_num int;
  latest_verdict text;
  latest_code int;
  latest_digest text;
  evt_input text;
  has_criteria boolean := false;
BEGIN
  IF NEW.status IS NOT DISTINCT FROM 'Done' AND NEW.passes IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'tasks: evidence';
  END IF;
  IF NEW.status = 'Working' AND OLD.status = 'Verifying' AND NEW.passes IS DISTINCT FROM false THEN
    RAISE EXCEPTION 'tasks: evidence';
  END IF;
  IF NEW.passes IS NOT DISTINCT FROM true AND (
    OLD.passes IS DISTINCT FROM true
    OR NEW.status IS NOT DISTINCT FROM 'Done'
    OR NEW.plan IS DISTINCT FROM OLD.plan
  ) THEN
    IF NEW.plan IS NULL THEN
      SELECT MAX(check_row.batch)
        INTO latest_batch
      FROM tasks.checks AS check_row
      WHERE check_row.task_id = NEW.id;
      IF latest_batch IS NULL OR EXISTS (
        SELECT 1
        FROM tasks.checks AS check_row
        WHERE check_row.task_id = NEW.id
          AND check_row.batch = latest_batch
          AND (check_row.code <> 0 OR check_row.verdict <> 'pass')
      ) THEN
        RAISE EXCEPTION 'tasks: evidence';
      END IF;
    ELSE
      IF jsonb_typeof(NEW.plan->'units') IS DISTINCT FROM 'array'
         OR jsonb_array_length(NEW.plan->'units') < 1 THEN
        RAISE EXCEPTION 'tasks: evidence';
      END IF;
      FOR unit_rec IN
        SELECT value AS unit_obj, ordinality::int AS u_idx
        FROM jsonb_array_elements(NEW.plan->'units') WITH ORDINALITY
      LOOP
        IF jsonb_typeof(unit_rec.unit_obj->'criteria') IS DISTINCT FROM 'array'
           OR jsonb_array_length(unit_rec.unit_obj->'criteria') < 1 THEN
          RAISE EXCEPTION 'tasks: evidence';
        END IF;
        unit_num := COALESCE((unit_rec.unit_obj->>'unit')::int, unit_rec.u_idx);
        FOR crit_rec IN
          SELECT value AS crit_obj, ordinality::int AS c_idx
          FROM jsonb_array_elements(unit_rec.unit_obj->'criteria') WITH ORDINALITY
        LOOP
          has_criteria := true;
          crit_num := COALESCE((crit_rec.crit_obj->>'criterion')::int, crit_rec.c_idx);
          IF EXISTS (
            SELECT 1 FROM tasks.review_findings AS rf
            WHERE rf.task_id = NEW.id
              AND rf.unit = unit_num
              AND rf.criterion = crit_num
              AND rf.blocking
              AND rf.state = 'open'
          ) THEN
            RAISE EXCEPTION 'tasks: evidence';
          END IF;
          IF NOT EXISTS (
            SELECT 1 FROM tasks.review_findings AS rf
            WHERE rf.task_id = NEW.id
              AND rf.unit = unit_num
              AND rf.criterion = crit_num
              AND rf.state = 'accepted'
          ) THEN
            SELECT check_row.verdict, check_row.code, check_row.digest
              INTO latest_verdict, latest_code, latest_digest
            FROM tasks.checks AS check_row
            WHERE check_row.task_id = NEW.id
              AND check_row.unit = unit_num
              AND check_row.criterion = crit_num
            ORDER BY check_row.batch DESC, check_row.seq DESC
            LIMIT 1;
            IF latest_verdict IS DISTINCT FROM 'pass'
               OR latest_code IS DISTINCT FROM 0
               OR latest_digest !~ '^[0-9a-f]{64}$'
               OR latest_digest IS DISTINCT FROM COALESCE(crit_rec.crit_obj->>'digest', '') THEN
              RAISE EXCEPTION 'tasks: evidence';
            END IF;
          END IF;
        END LOOP;
      END LOOP;
      IF NOT has_criteria THEN
        RAISE EXCEPTION 'tasks: evidence';
      END IF;
    END IF;
  END IF;
  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    RETURN NEW;
  END IF;
  SELECT event.from_status, event.to_status, event.input
    INTO prev, nxt, evt_input
  FROM tasks.task_events AS event
  WHERE event.task_id = NEW.id
  ORDER BY event.seq DESC
  LIMIT 1;
  IF evt_input = 'resume' AND EXISTS (
    SELECT 1 FROM tasks.approvals AS approval
    WHERE approval.task_id = NEW.id
      AND approval.state = 'pending'
  ) THEN
    RAISE EXCEPTION 'tasks: approval';
  END IF;
  IF prev IS DISTINCT FROM OLD.status OR nxt IS DISTINCT FROM NEW.status THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION tasks.keep_event()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  RAISE EXCEPTION 'tasks: event';
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER tasks_born
  AFTER INSERT ON tasks.tasks
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW
  EXECUTE FUNCTION tasks.born();

CREATE TRIGGER tasks_follow
  AFTER UPDATE ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.follow();

-- +goose StatementBegin
CREATE FUNCTION tasks.open_child_count()
RETURNS int
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
  SELECT count(*)::int
  FROM tasks.tasks
  WHERE parent_id IS NOT NULL
    AND status NOT IN ('Done', 'Failed');
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION tasks.open_child_count() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tasks.open_child_count() TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION tasks.limit_child()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
  parent_parent uuid;
  parent_status text;
  siblings int;
  open_children int;
BEGIN
  IF NEW.parent_id IS NULL THEN
    RETURN NEW;
  END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended('tasks:child', 0::bigint));
  SELECT task.parent_id, task.status
    INTO parent_parent, parent_status
  FROM tasks.tasks AS task
  WHERE task.id = NEW.parent_id;
  IF parent_status IS NULL THEN
    RAISE EXCEPTION 'tasks: no such task';
  END IF;
  IF parent_status IN ('Done', 'Failed') THEN
    RAISE EXCEPTION 'tasks: terminal';
  END IF;
  IF parent_parent IS NOT NULL THEN
    RAISE EXCEPTION 'tasks: depth';
  END IF;
  SELECT count(*)::int
    INTO siblings
  FROM tasks.tasks AS task
  WHERE task.parent_id = NEW.parent_id;
  IF siblings >= 5 THEN
    RAISE EXCEPTION 'tasks: fanout';
  END IF;
  open_children := tasks.open_child_count();
  IF open_children >= 6 THEN
    RAISE EXCEPTION 'tasks: ceiling';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_limit_child
  BEFORE INSERT ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.limit_child();

-- +goose StatementBegin
CREATE FUNCTION tasks.keep_parent()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.parent_id IS DISTINCT FROM OLD.parent_id THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_keep_parent
  BEFORE UPDATE OF parent_id ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_parent();

-- +goose StatementBegin
CREATE FUNCTION tasks.keep_result()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
  key text;
  keys int := 0;
  summary text;
  detail text;
  last_input text;
  last_reason text;
BEGIN
  IF TG_OP = 'INSERT' AND NEW.cause <> '' THEN
    RAISE EXCEPTION 'tasks: cause';
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.automation_id IS DISTINCT FROM OLD.automation_id THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.cause IS DISTINCT FROM OLD.cause THEN
    IF OLD.cause <> ''
       OR NEW.cause NOT IN ('parent_canceled', 'canceled', 'access', 'stalled', 'failing', 'review_limit', 'unknown')
       OR NEW.status <> 'Failed' THEN
      RAISE EXCEPTION 'tasks: cause';
    END IF;
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.worktree IS DISTINCT FROM OLD.worktree AND OLD.worktree <> '' THEN
    RAISE EXCEPTION 'tasks: worktree';
  END IF;
  IF TG_OP = 'UPDATE' AND OLD.status = 'Verifying' AND NEW.status = 'Working' THEN
    NEW.passes := false;
  END IF;
  IF TG_OP = 'UPDATE' AND OLD.status = 'Needs approval' AND NEW.status IN ('Working', 'Verifying') THEN
    SELECT event.input INTO last_input
    FROM tasks.task_events AS event
    WHERE event.task_id = NEW.id
    ORDER BY event.seq DESC
    LIMIT 1;
    IF last_input = 'approve' THEN
      SELECT approval.reason INTO last_reason
      FROM tasks.approvals AS approval
      WHERE approval.task_id = NEW.id
        AND approval.state = 'approved'
      ORDER BY approval.decided_at DESC NULLS LAST, approval.created_at DESC, approval.id DESC
      LIMIT 1;
      IF last_reason IN ('stalled', 'failing', 'review_limit', 'unknown') THEN
        NEW.stalls := 0;
        NEW.failures := 0;
        NEW.last_gap := '';
      END IF;
    END IF;
  END IF;
  IF TG_OP = 'INSERT' AND NEW.result IS NOT NULL AND NEW.status NOT IN ('Working', 'Verifying') THEN
    RAISE EXCEPTION 'tasks: result';
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.result IS DISTINCT FROM OLD.result THEN
    IF OLD.status IN ('Done', 'Failed')
       OR NEW.status NOT IN ('Working', 'Verifying')
       OR NEW.result IS NULL THEN
      RAISE EXCEPTION 'tasks: result';
    END IF;
  END IF;
  IF NEW.result IS NULL THEN
    RETURN NEW;
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.result IS NOT DISTINCT FROM OLD.result THEN
    RETURN NEW;
  END IF;
  IF jsonb_typeof(NEW.result) IS DISTINCT FROM 'object' THEN
    RAISE EXCEPTION 'tasks: result';
  END IF;
  FOR key IN SELECT jsonb_object_keys(NEW.result)
  LOOP
    keys := keys + 1;
    IF key NOT IN ('summary', 'detail') THEN
      RAISE EXCEPTION 'tasks: result';
    END IF;
  END LOOP;
  IF keys <> 2
     OR jsonb_typeof(NEW.result->'summary') IS DISTINCT FROM 'string'
     OR jsonb_typeof(NEW.result->'detail') IS DISTINCT FROM 'string' THEN
    RAISE EXCEPTION 'tasks: result';
  END IF;
  summary := NEW.result->>'summary';
  detail := NEW.result->>'detail';
  IF char_length(summary) < 1 OR char_length(summary) > 200 OR char_length(detail) > 32000 THEN
    RAISE EXCEPTION 'tasks: result';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_keep_result
  BEFORE INSERT OR UPDATE ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_result();

-- +goose StatementBegin
CREATE FUNCTION tasks.allow_fits(parent_allow text, child_allow text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog
AS $$
  SELECT COALESCE(bool_and(
    token = ANY (string_to_array(COALESCE(parent_allow, ''), ','))
  ), true)
  FROM unnest(string_to_array(COALESCE(child_allow, ''), ',')) AS token
  WHERE token <> '';
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION tasks.escrow_child()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  parent_budget bigint;
  parent_held bigint;
  parent_spent bigint;
  parent_allow text;
  parent_tainted boolean;
  parent_paused boolean;
  parent_status text;
BEGIN
  IF NEW.parent_id IS NULL THEN
    RETURN NEW;
  END IF;
  PERFORM pg_catalog.set_config('upmo.escrow_probe', NEW.parent_id::text, true);
  SELECT task.budget, task.held, task.spent, task.allow, task.tainted,
         task.paused, task.status
    INTO parent_budget, parent_held, parent_spent, parent_allow, parent_tainted,
         parent_paused, parent_status
  FROM tasks.tasks AS task
  WHERE task.id = NEW.parent_id
  FOR UPDATE;
  IF parent_budget IS NULL
     OR NEW.budget < 1
     OR parent_paused
     OR parent_status IN ('Done', 'Failed')
     OR parent_spent + parent_held + NEW.budget > parent_budget THEN
    RAISE EXCEPTION 'tasks: budget';
  END IF;
  IF NOT tasks.allow_fits(parent_allow, NEW.allow) THEN
    RAISE EXCEPTION 'tasks: authority';
  END IF;
  IF parent_tainted AND NOT NEW.tainted THEN
    RAISE EXCEPTION 'tasks: taint';
  END IF;
  UPDATE tasks.tasks
  SET held = held + NEW.budget
  WHERE id = NEW.parent_id;
  PERFORM pg_catalog.set_config('upmo.escrow_probe', '', true);
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_escrow_child
  BEFORE INSERT ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.escrow_child();

-- +goose StatementBegin
CREATE FUNCTION tasks.release_escrow()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.parent_id IS NOT NULL
     AND OLD.status NOT IN ('Done', 'Failed')
     AND NEW.status IN ('Done', 'Failed') THEN
    PERFORM pg_catalog.set_config('upmo.escrow_probe', NEW.parent_id::text, true);
    UPDATE tasks.tasks
    SET spent = spent + NEW.spent,
        held = held - NEW.budget,
        paused = paused OR (spent + NEW.spent >= budget)
    WHERE id = NEW.parent_id;
    PERFORM pg_catalog.set_config('upmo.escrow_probe', '', true);
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_release_escrow
  AFTER UPDATE OF status ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.release_escrow();

-- +goose StatementBegin
CREATE FUNCTION tasks.clear_terminal_claim()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.runner_owned
     AND OLD.status NOT IN ('Done', 'Failed')
     AND NEW.status IN ('Done', 'Failed') THEN
    NEW.reserved := 0;
    NEW.claim_token := NULL;
    NEW.lease_until := NULL;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_clear_terminal_claim
  BEFORE UPDATE OF status ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.clear_terminal_claim();

-- +goose StatementBegin
CREATE FUNCTION tasks.budget_notice()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  notice_task uuid;
  prev text;
BEGIN
  IF NOT NEW.paused OR OLD.paused THEN
    RETURN NEW;
  END IF;
  notice_task := COALESCE(NEW.parent_id, NEW.id);
  prev := current_setting('upmo.escrow_probe', true);
  IF prev IS NULL OR prev = '' THEN
    PERFORM pg_catalog.set_config('upmo.escrow_probe', notice_task::text, true);
  END IF;
  PERFORM 1 FROM tasks.tasks AS task
  WHERE task.id = notice_task
  FOR UPDATE;
  INSERT INTO tasks.chat_events (task_id, seq, body)
  SELECT notice_task,
         COALESCE((SELECT MAX(seq) FROM tasks.chat_events WHERE task_id = notice_task), 0) + 1,
         'Budget exhausted.';
  IF prev IS NULL OR prev = '' THEN
    PERFORM pg_catalog.set_config('upmo.escrow_probe', '', true);
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_budget_notice
  AFTER UPDATE OF paused ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.budget_notice();

-- +goose StatementBegin
CREATE FUNCTION tasks.keep_grant()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.budget IS DISTINCT FROM OLD.budget THEN
    IF NEW.parent_id IS NOT NULL OR NEW.budget <= OLD.budget THEN
      RAISE EXCEPTION 'tasks: budget';
    END IF;
    IF OLD.status IN ('Done', 'Failed') OR NEW.status IN ('Done', 'Failed') THEN
      RAISE EXCEPTION 'tasks: terminal';
    END IF;
  END IF;
  IF NEW.allow IS DISTINCT FROM OLD.allow THEN
    RAISE EXCEPTION 'tasks: authority';
  END IF;
  IF (OLD.tainted AND NOT NEW.tainted) OR (OLD.private_read AND NOT NEW.private_read) THEN
    RAISE EXCEPTION 'tasks: taint';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_keep_grant
  BEFORE UPDATE ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_grant();

-- +goose StatementBegin
CREATE FUNCTION tasks.spread_taint()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  child_ids text;
BEGIN
  IF NOT NEW.tainted OR (TG_OP = 'UPDATE' AND OLD.tainted) THEN
    RETURN NEW;
  END IF;
  IF NEW.parent_id IS NOT NULL THEN
    PERFORM pg_catalog.set_config('upmo.escrow_probe', NEW.parent_id::text, true);
    UPDATE tasks.tasks
    SET tainted = true
    WHERE id = NEW.parent_id
      AND NOT tainted;
    PERFORM pg_catalog.set_config('upmo.escrow_probe', '', true);
  END IF;
  PERFORM pg_catalog.set_config('upmo.escrow_child_of', NEW.id::text, true);
  SELECT COALESCE(string_agg(child.id::text, ','), '')
    INTO child_ids
  FROM tasks.tasks AS child
  WHERE child.parent_id = NEW.id
    AND NOT child.tainted;
  PERFORM pg_catalog.set_config('upmo.escrow_child_of', '', true);
  IF child_ids <> '' THEN
    PERFORM pg_catalog.set_config('upmo.escrow_probe', child_ids, true);
    UPDATE tasks.tasks
    SET tainted = true
    WHERE id = ANY (string_to_array(child_ids, ',')::uuid[])
      AND NOT tainted;
    PERFORM pg_catalog.set_config('upmo.escrow_probe', '', true);
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_spread_taint
  AFTER INSERT OR UPDATE OF tainted ON tasks.tasks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.spread_taint();

CREATE TRIGGER task_events_append_only
  BEFORE UPDATE OR DELETE ON tasks.task_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_event();

CREATE TRIGGER chat_events_append_only
  BEFORE UPDATE OR DELETE ON tasks.chat_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_event();

CREATE TRIGGER checks_append_only
  BEFORE UPDATE OR DELETE ON tasks.checks
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_event();

CREATE TRIGGER runs_cap
  BEFORE INSERT OR UPDATE ON tasks.runs
  FOR EACH ROW
  EXECUTE FUNCTION tasks.cap_run();

CREATE CONSTRAINT TRIGGER runs_born
  AFTER INSERT ON tasks.runs
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW
  EXECUTE FUNCTION tasks.run_born();

CREATE TRIGGER runs_follow
  AFTER UPDATE OF state ON tasks.runs
  FOR EACH ROW
  EXECUTE FUNCTION tasks.run_follow();

CREATE TRIGGER run_events_append_only
  BEFORE UPDATE OR DELETE ON tasks.run_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_event();

GRANT SELECT, INSERT ON tasks.task_events TO upmo_app;
GRANT SELECT, INSERT ON tasks.chat_events TO upmo_app;
GRANT SELECT ON tasks.checks TO upmo_app;
REVOKE INSERT, UPDATE, DELETE ON tasks.checks FROM upmo_app;
GRANT SELECT ON tasks.runs TO upmo_app;
GRANT INSERT (id, task_id, state, created_at) ON tasks.runs TO upmo_app;
GRANT UPDATE (state) ON tasks.runs TO upmo_app;
GRANT SELECT, INSERT ON tasks.run_events TO upmo_app;
REVOKE UPDATE, DELETE ON tasks.task_events FROM upmo_app;
REVOKE UPDATE, DELETE ON tasks.chat_events FROM upmo_app;
REVOKE UPDATE, DELETE ON tasks.checks FROM upmo_app;
REVOKE DELETE ON tasks.runs FROM upmo_app;
REVOKE UPDATE, DELETE ON tasks.run_events FROM upmo_app;

SELECT identity.protect('tasks.tasks');
REVOKE UPDATE (
  agent_id, agent_version, template_id, source_chat_id, up_to_line_id, dry_run,
  tainted, private_read, runner_owned, claim_token, lease_until, held, reserved, spent,
  passes, flow, plan_approval, plan, plan_digest, stalls, failures, replanned, last_gap
) ON tasks.tasks FROM upmo_app;

ALTER TABLE tasks.templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.templates FORCE ROW LEVEL SECURITY;
ALTER TABLE tasks.template_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.template_steps FORCE ROW LEVEL SECURITY;
ALTER TABLE tasks.template_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.template_deliveries FORCE ROW LEVEL SECURITY;

CREATE POLICY templates_select ON tasks.templates
  FOR SELECT
  USING (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY templates_insert ON tasks.templates
  FOR INSERT
  WITH CHECK (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY templates_update ON tasks.templates
  FOR UPDATE
  USING (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  )
  WITH CHECK (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY templates_delete ON tasks.templates
  FOR DELETE
  USING (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:delete' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY template_steps_select ON tasks.template_steps
  FOR SELECT
  USING (
    'task:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND EXISTS (
      SELECT 1 FROM tasks.templates AS template
      WHERE template.id = template_id
        AND template.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

CREATE POLICY template_steps_insert ON tasks.template_steps
  FOR INSERT
  WITH CHECK (
    ('task:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
     OR 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
    AND EXISTS (
      SELECT 1 FROM tasks.templates AS template
      WHERE template.id = template_id
        AND template.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

CREATE POLICY template_steps_delete ON tasks.template_steps
  FOR DELETE
  USING (
    ('task:delete' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
     OR 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
    AND EXISTS (
      SELECT 1 FROM tasks.templates AS template
      WHERE template.id = template_id
        AND template.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

CREATE POLICY template_deliveries_select ON tasks.template_deliveries
  FOR SELECT
  USING (
    'task:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND EXISTS (
      SELECT 1 FROM tasks.templates AS template
      WHERE template.id = template_id
        AND template.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

CREATE POLICY template_deliveries_insert ON tasks.template_deliveries
  FOR INSERT
  WITH CHECK (
    ('task:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
     OR 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
    AND EXISTS (
      SELECT 1 FROM tasks.templates AS template
      WHERE template.id = template_id
        AND template.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

CREATE POLICY template_deliveries_delete ON tasks.template_deliveries
  FOR DELETE
  USING (
    ('task:delete' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
     OR 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
    AND EXISTS (
      SELECT 1 FROM tasks.templates AS template
      WHERE template.id = template_id
        AND template.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

GRANT SELECT, INSERT, DELETE ON tasks.templates TO upmo_app;
GRANT UPDATE (goal, agent_id, budget, updated_at) ON tasks.templates TO upmo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON tasks.template_steps TO upmo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON tasks.template_deliveries TO upmo_app;

CREATE TABLE tasks.automations (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  owner_user_id uuid NOT NULL,
  template_id uuid NOT NULL REFERENCES tasks.templates (id),
  kind text NOT NULL,
  spec text NOT NULL,
  mode text NOT NULL DEFAULT 'on',
  failures int NOT NULL DEFAULT 0,
  reason text NOT NULL DEFAULT '',
  CONSTRAINT automations_kind_check CHECK (kind IN ('cron', 'event', 'webhook', 'channel')),
  CONSTRAINT automations_spec_check CHECK (char_length(spec) BETWEEN 1 AND 200),
  CONSTRAINT automations_mode_check CHECK (mode IN ('on', 'off')),
  CONSTRAINT automations_failures_check CHECK (failures BETWEEN 0 AND 3),
  CONSTRAINT automations_reason_check CHECK (reason IN ('', 'breaker', 'owner_left')),
  CONSTRAINT automations_live_check CHECK (mode = 'off' OR reason <> 'owner_left')
);

CREATE TABLE tasks.automation_fires (
  automation_id uuid NOT NULL REFERENCES tasks.automations (id) ON DELETE CASCADE,
  dedup_key text NOT NULL,
  task_id uuid NOT NULL REFERENCES tasks.tasks (id),
  PRIMARY KEY (automation_id, dedup_key),
  CONSTRAINT automation_fires_key_check CHECK (char_length(dedup_key) BETWEEN 1 AND 200)
);

ALTER TABLE tasks.automations ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.automations FORCE ROW LEVEL SECURITY;
ALTER TABLE tasks.automation_fires ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.automation_fires FORCE ROW LEVEL SECURITY;

CREATE POLICY automations_select ON tasks.automations
  FOR SELECT
  USING (
    'members:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    OR (
      owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
      AND 'task:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    )
  );

CREATE POLICY automations_insert ON tasks.automations
  FOR INSERT
  WITH CHECK (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY automations_update ON tasks.automations
  FOR UPDATE
  USING (
    (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
      AND 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
    OR 'members:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  )
  WITH CHECK (
    (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
      AND 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
    OR 'members:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY automation_fires_select ON tasks.automation_fires
  FOR SELECT
  USING (
    EXISTS (
      SELECT 1 FROM tasks.automations AS automation
      WHERE automation.id = automation_id
        AND automation.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
    AND 'task:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY automation_fires_insert ON tasks.automation_fires
  FOR INSERT
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM tasks.automations AS automation
      WHERE automation.id = automation_id
        AND automation.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
    AND 'task:create' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

-- +goose StatementBegin
CREATE FUNCTION tasks.automation_template(automation uuid)
RETURNS TABLE (goal text, budget bigint)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
  SELECT template.goal, template.budget
  FROM tasks.templates AS template
  JOIN tasks.automations AS automation_row ON automation_row.template_id = template.id
  WHERE automation_row.id = automation
    AND automation_row.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION tasks.automation_template(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tasks.automation_template(uuid) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION tasks.automation_uses(agents uuid[])
RETURNS TABLE (id uuid, goal text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
  SELECT automation_row.id, template.goal
  FROM tasks.automations AS automation_row
  JOIN tasks.templates AS template ON template.id = automation_row.template_id
  WHERE template.agent_id = ANY (agents)
    AND (
      'members:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
      OR (
        automation_row.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
        AND 'task:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
      )
    );
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION tasks.automation_uses(uuid[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tasks.automation_uses(uuid[]) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION tasks.own_template()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM tasks.templates
    WHERE id = NEW.template_id
      AND owner_user_id = NEW.owner_user_id
  ) THEN
    RAISE EXCEPTION 'tasks: template';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_own_template
  BEFORE INSERT ON tasks.automations
  FOR EACH ROW
  EXECUTE FUNCTION tasks.own_template();

GRANT SELECT, INSERT ON tasks.automations TO upmo_app;
GRANT UPDATE (mode, failures, reason, owner_user_id) ON tasks.automations TO upmo_app;
GRANT SELECT, INSERT ON tasks.automation_fires TO upmo_app;

-- tasks.open_child_count counts the open children of every task, for the
-- ceiling, so it must see rows the caller may not. It runs as the owner of the
-- table, and this policy admits that role to the open children and nothing
-- else, like chat.deltas_sweep_select. It holds for an owner that is neither a
-- superuser nor BYPASSRLS, since FORCE applies to an owner too. Without it
-- such an owner would count only what the caller may see.
CREATE POLICY tasks_owner_open_children ON tasks.tasks
  FOR SELECT
  USING (
    parent_id IS NOT NULL
    AND status NOT IN ('Done', 'Failed')
    AND current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.tasks'::regclass))
  );

CREATE POLICY tasks_owner_escrow_select ON tasks.tasks
  FOR SELECT
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.tasks'::regclass))
    AND (
      id IN (
        SELECT probe.item::uuid
        FROM unnest(string_to_array(current_setting('upmo.escrow_probe', true), ',')) AS probe(item)
        WHERE probe.item ~ '^[0-9a-fA-F-]{36}$'
      )
      OR parent_id = NULLIF(current_setting('upmo.escrow_child_of', true), '')::uuid
    )
  );

CREATE POLICY tasks_owner_escrow_update ON tasks.tasks
  FOR UPDATE
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.tasks'::regclass))
    AND id IN (
      SELECT probe.item::uuid
      FROM unnest(string_to_array(current_setting('upmo.escrow_probe', true), ',')) AS probe(item)
      WHERE probe.item ~ '^[0-9a-fA-F-]{36}$'
    )
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.tasks'::regclass))
    AND id IN (
      SELECT probe.item::uuid
      FROM unnest(string_to_array(current_setting('upmo.escrow_probe', true), ',')) AS probe(item)
      WHERE probe.item ~ '^[0-9a-fA-F-]{36}$'
    )
  );

CREATE POLICY chat_events_owner_budget_select ON tasks.chat_events
  FOR SELECT
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.chat_events'::regclass))
    AND task_id IN (
      SELECT probe.item::uuid
      FROM unnest(string_to_array(current_setting('upmo.escrow_probe', true), ',')) AS probe(item)
      WHERE probe.item ~ '^[0-9a-fA-F-]{36}$'
    )
  );

CREATE POLICY chat_events_owner_budget_insert ON tasks.chat_events
  FOR INSERT
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.chat_events'::regclass))
    AND body = 'Budget exhausted.'
    AND task_id IN (
      SELECT probe.item::uuid
      FROM unnest(string_to_array(current_setting('upmo.escrow_probe', true), ',')) AS probe(item)
      WHERE probe.item ~ '^[0-9a-fA-F-]{36}$'
    )
  );

-- tasks.automation_exists checks whether an automation id exists when setting
-- a spend limit, returning only a boolean so callers without task:view or
-- members:manage never read automation columns. Because tasks.automations has
-- FORCE ROW LEVEL SECURITY, an owner that is neither a superuser nor BYPASSRLS
-- needs this policy for the SECURITY DEFINER function to see rows across
-- owners. Setting upmo.automation_probe transaction-locally around the EXISTS
-- check and clearing it before returning limits the owner policy to that single
-- probed id; without the setting, the table owner sees no automation rows.
-- Nothing outside tasks.automation_exists reads automations as the table owner;
-- upmo_app holds EXECUTE only on the function.
-- +goose StatementBegin
CREATE FUNCTION tasks.automation_exists(p_id uuid)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_exists boolean;
BEGIN
  IF p_id IS NULL THEN
    RETURN false;
  END IF;

  PERFORM pg_catalog.set_config('upmo.automation_probe', p_id::text, true);

  SELECT EXISTS (
    SELECT 1
    FROM tasks.automations
    WHERE id = p_id
  )
  INTO v_exists;

  PERFORM pg_catalog.set_config('upmo.automation_probe', '', true);

  RETURN v_exists;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION tasks.automation_exists(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tasks.automation_exists(uuid) TO upmo_app;

CREATE POLICY automations_owner_exists ON tasks.automations
  FOR SELECT
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.automations'::regclass))
    AND id = NULLIF(current_setting('upmo.automation_probe', true), '')::uuid
  );

-- A task event or a chat line announces itself to the stream hub of every
-- brain process. The payload carries ids only: the hub reads the rows. NOTIFY
-- is queued in the writing transaction and sent when it commits, so a
-- rollback sends nothing. It is not durable: the hub replays from the tables
-- after a reconnect and on a timer.

-- +goose StatementBegin
CREATE FUNCTION tasks.stream_notify()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  PERFORM pg_notify('task_stream', json_build_object('task', NEW.task_id, 'id', NEW.id)::text);
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER task_events_stream_notify
  AFTER INSERT ON tasks.task_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.stream_notify();

CREATE TRIGGER chat_events_stream_notify
  AFTER INSERT ON tasks.chat_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.stream_notify();

CREATE TABLE tasks.approvals (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id),
  user_id uuid NOT NULL,
  tool text NOT NULL,
  reason text NOT NULL,
  action text NOT NULL,
  preview text NOT NULL DEFAULT '',
  answer text NOT NULL DEFAULT '',
  from_status text NOT NULL DEFAULT 'Working',
  calls jsonb[] NOT NULL DEFAULT '{}'::jsonb[],
  untrusted_origin boolean NOT NULL DEFAULT false,
  state text NOT NULL DEFAULT 'pending',
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  CONSTRAINT approvals_tool_check CHECK (char_length(tool) BETWEEN 1 AND 64),
  CONSTRAINT approvals_reason_check CHECK (
    reason IN ('ask', 'expired', 'plan', 'question', 'trifecta', 'writeguard', 'stalled', 'failing', 'review_limit', 'unknown')
  ),
  CONSTRAINT approvals_action_check CHECK (char_length(action) BETWEEN 1 AND 200),
  CONSTRAINT approvals_preview_check CHECK (char_length(preview) <= 4000),
  CONSTRAINT approvals_answer_check CHECK (char_length(answer) <= 4000),
  CONSTRAINT approvals_from_status_check CHECK (from_status IN ('Planning', 'Working', 'Verifying')),
  CONSTRAINT approvals_state_check CHECK (state IN ('pending', 'approved', 'denied', 'closed')),
  CONSTRAINT approvals_decided_check CHECK ((state = 'pending') = (decided_at IS NULL))
);

CREATE UNIQUE INDEX approvals_one_pending
  ON tasks.approvals (task_id)
  WHERE state = 'pending';

CREATE INDEX approvals_user_idx ON tasks.approvals (user_id, created_at DESC);

ALTER TABLE tasks.approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.approvals FORCE ROW LEVEL SECURITY;

CREATE POLICY approvals_select ON tasks.approvals
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('view', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

CREATE POLICY approvals_insert ON tasks.approvals
  FOR INSERT
  WITH CHECK (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND task.owner_user_id = user_id
    )
  );

CREATE POLICY approvals_update ON tasks.approvals
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

CREATE POLICY approvals_close ON tasks.approvals
  FOR UPDATE
  USING (
    state = 'pending'
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND task.status IN ('Done', 'Failed')
    )
  )
  WITH CHECK (state = 'closed');

CREATE POLICY approvals_close_read ON tasks.approvals
  FOR SELECT
  USING (
    state IN ('pending', 'closed')
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND task.status IN ('Done', 'Failed')
        AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
    )
  );

-- +goose StatementBegin
CREATE FUNCTION tasks.decide_once()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF OLD.state <> 'pending' THEN
    RAISE EXCEPTION 'tasks: approval';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER approvals_decide_once
  BEFORE UPDATE ON tasks.approvals
  FOR EACH ROW
  EXECUTE FUNCTION tasks.decide_once();

GRANT SELECT, INSERT ON tasks.approvals TO upmo_app;
GRANT UPDATE (state, decided_at, answer) ON tasks.approvals TO upmo_app;

CREATE TABLE tasks.findings (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id) ON DELETE CASCADE,
  run_id uuid REFERENCES tasks.runs (id) ON DELETE CASCADE,
  kind text NOT NULL DEFAULT 'finding',
  title text NOT NULL DEFAULT '',
  body text NOT NULL,
  source_ref text NOT NULL DEFAULT '',
  untrusted boolean NOT NULL DEFAULT false,
  private_read boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT findings_kind_check CHECK (kind IN ('finding', 'output', 'tool_output')),
  CONSTRAINT findings_body_check CHECK (octet_length(body) <= 262144)
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
      AND (NOT tasks.findings.private_read OR task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  ));

GRANT SELECT ON tasks.findings TO upmo_app;

CREATE TABLE tasks.turns (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id) ON DELETE CASCADE,
  run_id uuid NOT NULL REFERENCES tasks.runs (id) ON DELETE CASCADE,
  seq int NOT NULL,
  role text NOT NULL,
  actor text NOT NULL,
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
  CONSTRAINT turns_role_check CHECK (role IN ('user', 'assistant', 'tool', 'blocked', 'simulated')),
  CONSTRAINT turns_actor_check CHECK (actor IN ('person', 'planner', 'worker', 'verifier', 'core')),
  CONSTRAINT turns_body_check CHECK (octet_length(body) <= 262144)
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
      AND (NOT tasks.turns.private_read OR task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  ));

GRANT SELECT ON tasks.turns TO upmo_app;

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

CREATE TABLE tasks.settlements (
  task_id uuid NOT NULL REFERENCES tasks.tasks (id) ON DELETE CASCADE,
  claim_token uuid NOT NULL,
  amount bigint NOT NULL CHECK (amount >= 0),
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (task_id, claim_token)
);

ALTER TABLE tasks.settlements ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.settlements FORCE ROW LEVEL SECURITY;

CREATE POLICY settlements_owner ON tasks.settlements
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.settlements'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.settlements'::regclass))
  );

CREATE TABLE tasks.run_history (
  run_id uuid PRIMARY KEY REFERENCES tasks.runs (id) ON DELETE CASCADE,
  history jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT run_history_array_check CHECK (jsonb_typeof(history) = 'array'),
  CONSTRAINT run_history_size_check CHECK (octet_length(history::text) <= 2097152)
);

ALTER TABLE tasks.run_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.run_history FORCE ROW LEVEL SECURITY;

CREATE POLICY run_history_owner ON tasks.run_history
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.run_history'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'tasks.run_history'::regclass))
  );

-- upmo_runner writes the runner columns upmo_app may not: the check batches
-- and passes (Record), the plan and brake counters, the spend and the taint,
-- and the agent loop's turn commit. Brain sets the task owner's settings on
-- each of those transactions, and every runner policy admits only rows of
-- the task whose owner is upmo.user_id, the same test the statements make.
GRANT USAGE ON SCHEMA tasks TO upmo_runner;
GRANT EXECUTE ON FUNCTION identity.may(text, text, uuid, text, uuid, uuid) TO upmo_runner;
GRANT SELECT ON tasks.tasks TO upmo_runner;
GRANT UPDATE (
  status, cause, passes, plan, plan_digest, stalls, failures, replanned, last_gap,
  spent, reserved, paused, tainted, private_read, claim_token, lease_until, updated_at
) ON tasks.tasks TO upmo_runner;
GRANT SELECT, INSERT ON tasks.checks TO upmo_runner;
GRANT SELECT ON tasks.review_findings TO upmo_runner;
GRANT SELECT, INSERT ON tasks.findings TO upmo_runner;
GRANT SELECT, INSERT ON tasks.turns TO upmo_runner;
GRANT SELECT, INSERT ON tasks.approvals TO upmo_runner;
GRANT UPDATE (state, decided_at) ON tasks.approvals TO upmo_runner;
GRANT SELECT, INSERT ON tasks.task_events TO upmo_runner;
GRANT SELECT, INSERT ON tasks.chat_events TO upmo_runner;
GRANT SELECT ON tasks.runs TO upmo_runner;
GRANT UPDATE (turn_seq, msg_seq) ON tasks.runs TO upmo_runner;
GRANT SELECT, INSERT ON tasks.settlements TO upmo_runner;
GRANT SELECT, INSERT, UPDATE (history, updated_at) ON tasks.run_history TO upmo_runner;

CREATE POLICY tasks_runner_select ON tasks.tasks
  FOR SELECT TO upmo_runner
  USING (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

CREATE POLICY tasks_runner_update ON tasks.tasks
  FOR UPDATE TO upmo_runner
  USING (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  WITH CHECK (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

CREATE POLICY checks_runner_select ON tasks.checks
  FOR SELECT TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY checks_runner_insert ON tasks.checks
  FOR INSERT TO upmo_runner
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY review_findings_runner_select ON tasks.review_findings
  FOR SELECT TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY findings_runner ON tasks.findings
  FOR ALL TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (
    kind IN ('output', 'tool_output')
    AND EXISTS (
      SELECT 1 FROM tasks.tasks AS task
      WHERE task.id = task_id
        AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
  );

CREATE POLICY turns_runner ON tasks.turns
  FOR ALL TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY task_events_runner ON tasks.task_events
  FOR ALL TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY chat_events_runner ON tasks.chat_events
  FOR ALL TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY chat_events_runner_owner_only ON tasks.chat_events
  AS RESTRICTIVE FOR ALL TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY approvals_runner ON tasks.approvals
  FOR ALL TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY runs_runner_select ON tasks.runs
  FOR SELECT TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY runs_runner_update ON tasks.runs
  FOR UPDATE TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY settlements_runner_select ON tasks.settlements
  FOR SELECT TO upmo_runner
  USING (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY settlements_runner_insert ON tasks.settlements
  FOR INSERT TO upmo_runner
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY run_history_runner_select ON tasks.run_history
  FOR SELECT TO upmo_runner
  USING (EXISTS (
    SELECT 1
    FROM tasks.runs AS run
    JOIN tasks.tasks AS task ON task.id = run.task_id
    WHERE run.id = run_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY run_history_runner_insert ON tasks.run_history
  FOR INSERT TO upmo_runner
  WITH CHECK (EXISTS (
    SELECT 1
    FROM tasks.runs AS run
    JOIN tasks.tasks AS task ON task.id = run.task_id
    WHERE run.id = run_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY run_history_runner_update ON tasks.run_history
  FOR UPDATE TO upmo_runner
  USING (EXISTS (
    SELECT 1
    FROM tasks.runs AS run
    JOIN tasks.tasks AS task ON task.id = run.task_id
    WHERE run.id = run_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1
    FROM tasks.runs AS run
    JOIN tasks.tasks AS task ON task.id = run.task_id
    WHERE run.id = run_id
      AND task.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

-- +goose Down

DROP POLICY IF EXISTS chat_events_runner_owner_only ON tasks.chat_events;
DROP POLICY IF EXISTS chat_events_runner ON tasks.chat_events;
REVOKE SELECT, INSERT ON tasks.chat_events FROM upmo_runner;

DROP TABLE IF EXISTS tasks.run_history;
DROP TABLE IF EXISTS tasks.settlements;
DROP TABLE IF EXISTS tasks.action_logs;
DROP TABLE IF EXISTS tasks.turns;
DROP TABLE IF EXISTS tasks.findings;
DROP TRIGGER IF EXISTS approvals_decide_once ON tasks.approvals;
DROP FUNCTION IF EXISTS tasks.decide_once();
DROP TABLE IF EXISTS tasks.approvals;
DROP TRIGGER IF EXISTS chat_events_stream_notify ON tasks.chat_events;
DROP TRIGGER IF EXISTS task_events_stream_notify ON tasks.task_events;
DROP FUNCTION IF EXISTS tasks.stream_notify();
DROP POLICY IF EXISTS automations_owner_exists ON tasks.automations;
DROP FUNCTION IF EXISTS tasks.automation_exists(uuid);
DROP POLICY IF EXISTS tasks_owner_open_children ON tasks.tasks;
DROP POLICY IF EXISTS tasks_owner_escrow_select ON tasks.tasks;
DROP POLICY IF EXISTS tasks_owner_escrow_update ON tasks.tasks;
DROP POLICY IF EXISTS chat_events_owner_budget_select ON tasks.chat_events;
DROP POLICY IF EXISTS chat_events_owner_budget_insert ON tasks.chat_events;
DROP TABLE IF EXISTS tasks.automation_fires;
DROP TABLE IF EXISTS tasks.automations;
DROP FUNCTION IF EXISTS tasks.automation_uses(uuid[]);
DROP FUNCTION IF EXISTS tasks.automation_template(uuid);
DROP FUNCTION IF EXISTS tasks.own_template();
DROP INDEX IF EXISTS tasks.tasks_worktree_writer;
DROP TRIGGER IF EXISTS tasks_keep_result ON tasks.tasks;
DROP FUNCTION IF EXISTS tasks.keep_result();
DROP TRIGGER IF EXISTS tasks_spread_taint ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_keep_grant ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_budget_notice ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_clear_terminal_claim ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_release_escrow ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_escrow_child ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_keep_parent ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_limit_child ON tasks.tasks;
DROP FUNCTION IF EXISTS tasks.spread_taint();
DROP FUNCTION IF EXISTS tasks.keep_grant();
DROP FUNCTION IF EXISTS tasks.budget_notice();
DROP FUNCTION IF EXISTS tasks.clear_terminal_claim();
DROP FUNCTION IF EXISTS tasks.release_escrow();
DROP FUNCTION IF EXISTS tasks.escrow_child();
DROP FUNCTION IF EXISTS tasks.allow_fits(text, text);
DROP FUNCTION IF EXISTS tasks.keep_parent();
DROP FUNCTION IF EXISTS tasks.limit_child();
DROP FUNCTION IF EXISTS tasks.open_child_count();

DROP TRIGGER IF EXISTS run_events_append_only ON tasks.run_events;
DROP TRIGGER IF EXISTS runs_follow ON tasks.runs;
DROP TRIGGER IF EXISTS runs_born ON tasks.runs;
DROP TRIGGER IF EXISTS runs_cap ON tasks.runs;
DROP TRIGGER IF EXISTS checks_append_only ON tasks.checks;
DROP TRIGGER IF EXISTS chat_events_append_only ON tasks.chat_events;
DROP TRIGGER IF EXISTS task_events_append_only ON tasks.task_events;
DROP TRIGGER IF EXISTS tasks_follow ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_born ON tasks.tasks;
DROP FUNCTION IF EXISTS tasks.run_follow();
DROP FUNCTION IF EXISTS tasks.run_born();
DROP FUNCTION IF EXISTS tasks.cap_run();
DROP FUNCTION IF EXISTS tasks.keep_event();
DROP FUNCTION IF EXISTS tasks.follow();
DROP FUNCTION IF EXISTS tasks.born();
DROP TABLE IF EXISTS tasks.run_events;
DROP TABLE IF EXISTS tasks.runs;
DROP TABLE IF EXISTS tasks.checks;
DROP TABLE IF EXISTS tasks.chat_events;
DROP TABLE IF EXISTS tasks.task_events;
DROP TABLE IF EXISTS tasks.review_findings;
DROP TABLE IF EXISTS tasks.tasks;
DROP TABLE IF EXISTS tasks.template_deliveries;
DROP TABLE IF EXISTS tasks.template_steps;
DROP TABLE IF EXISTS tasks.templates;
