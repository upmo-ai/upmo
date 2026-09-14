-- +goose Up

CREATE TABLE tasks.tasks (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL DEFAULT 'task',
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL DEFAULT 'private',
  visibility_team_id uuid,
  goal text NOT NULL,
  status text NOT NULL,
  parent_id uuid REFERENCES tasks.tasks (id),
  budget int NOT NULL DEFAULT 0,
  held int NOT NULL DEFAULT 0,
  spent int NOT NULL DEFAULT 0,
  paused boolean NOT NULL DEFAULT false,
  allow text NOT NULL DEFAULT '',
  tainted boolean NOT NULL DEFAULT false,
  cause text NOT NULL DEFAULT '',
  result jsonb,
  worktree text NOT NULL DEFAULT '',
  automation_id uuid,
  passes boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT tasks_type_check CHECK (object_type = 'task'),
  CONSTRAINT tasks_goal_check CHECK (char_length(goal) BETWEEN 1 AND 200),
  CONSTRAINT tasks_status_check CHECK (status IN ('Preparing', 'Planning', 'Working', 'Checking', 'Done', 'Failed')),
  CONSTRAINT tasks_visibility_check CHECK (
    (visibility IN ('private', 'everyone') AND visibility_team_id IS NULL)
    OR (visibility = 'team' AND visibility_team_id IS NOT NULL)
  ),
  CONSTRAINT tasks_parent_check CHECK (parent_id IS NULL OR parent_id <> id),
  CONSTRAINT tasks_budget_check CHECK (budget BETWEEN 0 AND 1000000),
  CONSTRAINT tasks_held_check CHECK (held BETWEEN 0 AND budget),
  CONSTRAINT tasks_spent_check CHECK (spent BETWEEN 0 AND budget),
  CONSTRAINT tasks_allow_check CHECK (char_length(allow) <= 400),
  CONSTRAINT tasks_cause_check CHECK (
    cause = '' OR (cause = 'parent_cancelled' AND status = 'Failed')
  ),
  CONSTRAINT tasks_worktree_check CHECK (char_length(worktree) <= 80)
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
  at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (task_id, seq),
  CONSTRAINT task_events_seq_check CHECK (seq > 0),
  CONSTRAINT task_events_edge_check CHECK (
    (input = 'open' AND from_status = '' AND to_status = 'Preparing' AND seq = 1)
    OR (input = 'advance' AND (
      (from_status = 'Preparing' AND to_status = 'Planning')
      OR (from_status = 'Planning' AND to_status = 'Working')
      OR (from_status = 'Working' AND to_status = 'Checking')
    ))
    OR (input = 'fail' AND to_status = 'Failed' AND from_status IN ('Preparing', 'Planning', 'Working', 'Checking'))
    OR (input = 'pass' AND from_status = 'Checking' AND to_status = 'Done')
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
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (task_id, batch, seq),
  CONSTRAINT checks_batch_check CHECK (batch > 0),
  CONSTRAINT checks_seq_check CHECK (seq > 0),
  CONSTRAINT checks_name_check CHECK (char_length(name) BETWEEN 1 AND 80)
);

CREATE TABLE tasks.runs (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id),
  state text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT runs_state_check CHECK (state IN ('queued', 'admitted', 'ended'))
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
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

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

CREATE POLICY checks_insert ON tasks.checks
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM tasks.tasks AS task
    WHERE task.id = task_id
      AND identity.may('edit', task.object_type, task.owner_user_id, task.visibility, task.visibility_team_id, task.id)
  ));

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
  IF NEW.status IS DISTINCT FROM 'Preparing' THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM tasks.task_events AS event
    WHERE event.task_id = NEW.id
      AND event.input = 'open'
      AND event.from_status = ''
      AND event.to_status = 'Preparing'
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
BEGIN
  IF NEW.status IS NOT DISTINCT FROM 'Done' AND NEW.passes IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'tasks: evidence';
  END IF;
  IF NEW.passes IS NOT DISTINCT FROM true AND OLD.passes IS DISTINCT FROM true THEN
    SELECT MAX(check_row.batch)
      INTO latest_batch
    FROM tasks.checks AS check_row
    WHERE check_row.task_id = NEW.id;
    IF latest_batch IS NULL OR EXISTS (
      SELECT 1
      FROM tasks.checks AS check_row
      WHERE check_row.task_id = NEW.id
        AND check_row.batch = latest_batch
        AND check_row.code <> 0
    ) THEN
      RAISE EXCEPTION 'tasks: evidence';
    END IF;
  END IF;
  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    RETURN NEW;
  END IF;
  SELECT event.from_status, event.to_status
    INTO prev, nxt
  FROM tasks.task_events AS event
  WHERE event.task_id = NEW.id
  ORDER BY event.seq DESC
  LIMIT 1;
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
BEGIN
  IF TG_OP = 'INSERT' AND NEW.cause <> '' THEN
    RAISE EXCEPTION 'tasks: cause';
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.automation_id IS DISTINCT FROM OLD.automation_id THEN
    RAISE EXCEPTION 'tasks: writer';
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.cause IS DISTINCT FROM OLD.cause THEN
    IF OLD.cause <> '' OR NEW.cause <> 'parent_cancelled' OR NEW.status <> 'Failed' THEN
      RAISE EXCEPTION 'tasks: cause';
    END IF;
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.worktree IS DISTINCT FROM OLD.worktree AND OLD.worktree <> '' THEN
    RAISE EXCEPTION 'tasks: worktree';
  END IF;
  IF TG_OP = 'UPDATE' AND OLD.result IS NOT NULL AND NEW.result IS DISTINCT FROM OLD.result THEN
    RAISE EXCEPTION 'tasks: result';
  END IF;
  IF NEW.result IS NULL THEN
    RETURN NEW;
  END IF;
  IF TG_OP = 'UPDATE' AND OLD.result IS NOT NULL THEN
    RETURN NEW;
  END IF;
  IF NEW.parent_id IS NULL OR jsonb_typeof(NEW.result) IS DISTINCT FROM 'object' THEN
    RAISE EXCEPTION 'tasks: result';
  END IF;
  FOR key IN SELECT jsonb_object_keys(NEW.result)
  LOOP
    keys := keys + 1;
    IF key NOT IN ('summary', 'detail') THEN
      RAISE EXCEPTION 'tasks: result';
    END IF;
  END LOOP;
  IF keys <> 2 THEN
    RAISE EXCEPTION 'tasks: result';
  END IF;
  summary := NEW.result->>'summary';
  detail := COALESCE(NEW.result->>'detail', '');
  IF char_length(summary) < 1 OR char_length(summary) > 200 OR char_length(detail) > 2000 THEN
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
SET search_path = pg_catalog
AS $$
DECLARE
  parent_budget int;
  parent_held int;
  parent_allow text;
  parent_tainted boolean;
BEGIN
  IF NEW.parent_id IS NULL THEN
    RETURN NEW;
  END IF;
  SELECT task.budget, task.held, task.allow, task.tainted
    INTO parent_budget, parent_held, parent_allow, parent_tainted
  FROM tasks.tasks AS task
  WHERE task.id = NEW.parent_id;
  IF parent_budget IS NULL
     OR NEW.budget < 1
     OR parent_held + NEW.budget > parent_budget THEN
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
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.parent_id IS NOT NULL
     AND OLD.status NOT IN ('Done', 'Failed')
     AND NEW.status IN ('Done', 'Failed') THEN
    UPDATE tasks.tasks
    SET held = held - NEW.budget
    WHERE id = NEW.parent_id;
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
CREATE FUNCTION tasks.budget_notice()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
  notice_task uuid;
BEGIN
  IF NOT NEW.paused OR OLD.paused THEN
    RETURN NEW;
  END IF;
  notice_task := COALESCE(NEW.parent_id, NEW.id);
  INSERT INTO tasks.chat_events (task_id, seq, body)
  SELECT notice_task,
         COALESCE((SELECT MAX(seq) FROM tasks.chat_events WHERE task_id = notice_task), 0) + 1,
         'Budget exhausted.';
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
    IF OLD.budget <> 0 OR NEW.parent_id IS NOT NULL OR NEW.budget < 1 THEN
      RAISE EXCEPTION 'tasks: budget';
    END IF;
  END IF;
  IF NEW.allow IS DISTINCT FROM OLD.allow THEN
    RAISE EXCEPTION 'tasks: authority';
  END IF;
  IF OLD.tainted AND NOT NEW.tainted THEN
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
SET search_path = pg_catalog
AS $$
BEGIN
  IF NOT NEW.tainted OR OLD.tainted THEN
    RETURN NEW;
  END IF;
  UPDATE tasks.tasks
  SET tainted = true
  WHERE id = NEW.parent_id
    AND NOT tainted;
  UPDATE tasks.tasks
  SET tainted = true
  WHERE parent_id = NEW.id
    AND NOT tainted;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_spread_taint
  AFTER UPDATE OF tainted ON tasks.tasks
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
GRANT SELECT, INSERT ON tasks.checks TO upmo_app;
GRANT SELECT, INSERT ON tasks.runs TO upmo_app;
GRANT UPDATE (state) ON tasks.runs TO upmo_app;
GRANT SELECT, INSERT ON tasks.run_events TO upmo_app;
REVOKE UPDATE, DELETE ON tasks.task_events FROM upmo_app;
REVOKE UPDATE, DELETE ON tasks.chat_events FROM upmo_app;
REVOKE UPDATE, DELETE ON tasks.checks FROM upmo_app;
REVOKE DELETE ON tasks.runs FROM upmo_app;
REVOKE UPDATE, DELETE ON tasks.run_events FROM upmo_app;

SELECT identity.protect('tasks.tasks');

CREATE TABLE tasks.templates (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  owner_user_id uuid NOT NULL,
  goal text NOT NULL,
  agent_id uuid NOT NULL,
  budget int NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT templates_goal_check CHECK (char_length(goal) BETWEEN 1 AND 200),
  CONSTRAINT templates_budget_check CHECK (budget BETWEEN 1 AND 1000000)
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
RETURNS TABLE (goal text, budget int)
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

-- +goose Down

DROP TABLE IF EXISTS tasks.automation_fires;
DROP TABLE IF EXISTS tasks.automations;
DROP FUNCTION IF EXISTS tasks.automation_template(uuid);
DROP FUNCTION IF EXISTS tasks.own_template();
DROP INDEX IF EXISTS tasks.tasks_worktree_writer;
DROP TRIGGER IF EXISTS tasks_keep_result ON tasks.tasks;
DROP FUNCTION IF EXISTS tasks.keep_result();
DROP TRIGGER IF EXISTS tasks_spread_taint ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_keep_grant ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_budget_notice ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_release_escrow ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_escrow_child ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_keep_parent ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_limit_child ON tasks.tasks;
DROP FUNCTION IF EXISTS tasks.spread_taint();
DROP FUNCTION IF EXISTS tasks.keep_grant();
DROP FUNCTION IF EXISTS tasks.budget_notice();
DROP FUNCTION IF EXISTS tasks.release_escrow();
DROP FUNCTION IF EXISTS tasks.escrow_child();
DROP FUNCTION IF EXISTS tasks.allow_fits(text, text);
DROP FUNCTION IF EXISTS tasks.keep_parent();
DROP FUNCTION IF EXISTS tasks.limit_child();
DROP FUNCTION IF EXISTS tasks.open_child_count();

DROP TABLE IF EXISTS tasks.template_deliveries;
DROP TABLE IF EXISTS tasks.template_steps;
DROP TABLE IF EXISTS tasks.templates;

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
DROP TABLE IF EXISTS tasks.tasks;
