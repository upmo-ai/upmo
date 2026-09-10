-- +goose Up

CREATE TABLE tasks.tasks (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL DEFAULT 'task',
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL DEFAULT 'private',
  visibility_team_id uuid,
  goal text NOT NULL,
  status text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT tasks_type_check CHECK (object_type = 'task'),
  CONSTRAINT tasks_goal_check CHECK (char_length(goal) BETWEEN 1 AND 200),
  CONSTRAINT tasks_status_check CHECK (status IN ('Preparing', 'Planning', 'Working', 'Checking', 'Done', 'Failed')),
  CONSTRAINT tasks_visibility_check CHECK (
    (visibility IN ('private', 'everyone') AND visibility_team_id IS NULL)
    OR (visibility = 'team' AND visibility_team_id IS NOT NULL)
  )
);

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

ALTER TABLE tasks.task_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.task_events FORCE ROW LEVEL SECURITY;
ALTER TABLE tasks.chat_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.chat_events FORCE ROW LEVEL SECURITY;

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
BEGIN
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

CREATE TRIGGER task_events_append_only
  BEFORE UPDATE OR DELETE ON tasks.task_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_event();

CREATE TRIGGER chat_events_append_only
  BEFORE UPDATE OR DELETE ON tasks.chat_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.keep_event();

GRANT SELECT, INSERT ON tasks.task_events TO upmo_app;
GRANT SELECT, INSERT ON tasks.chat_events TO upmo_app;
REVOKE UPDATE, DELETE ON tasks.task_events FROM upmo_app;
REVOKE UPDATE, DELETE ON tasks.chat_events FROM upmo_app;

SELECT identity.protect('tasks.tasks');

-- +goose Down

DROP TRIGGER IF EXISTS chat_events_append_only ON tasks.chat_events;
DROP TRIGGER IF EXISTS task_events_append_only ON tasks.task_events;
DROP TRIGGER IF EXISTS tasks_follow ON tasks.tasks;
DROP TRIGGER IF EXISTS tasks_born ON tasks.tasks;
DROP FUNCTION IF EXISTS tasks.keep_event();
DROP FUNCTION IF EXISTS tasks.follow();
DROP FUNCTION IF EXISTS tasks.born();
DROP TABLE IF EXISTS tasks.chat_events;
DROP TABLE IF EXISTS tasks.task_events;
DROP TABLE IF EXISTS tasks.tasks;
