-- +goose Up

-- A person's decision on a paused task gets its own event inputs, so the
-- history tells an approval from a denial. A task that ends while it waits
-- closes its approval instead: the row is settled without a decision and,
-- like a decision, never changes again.
ALTER TABLE tasks.task_events DROP CONSTRAINT task_events_edge_check;
ALTER TABLE tasks.task_events ADD CONSTRAINT task_events_edge_check CHECK (
  (input = 'open' AND from_status = '' AND to_status = 'Preparing' AND seq = 1)
  OR (input = 'advance' AND (
    (from_status = 'Preparing' AND to_status = 'Working')
    OR (from_status = 'Planning' AND to_status = 'Working')
    OR (from_status = 'Working' AND to_status = 'Checking')
  ))
  OR (input = 'plan' AND from_status = 'Preparing' AND to_status = 'Planning')
  OR (input = 'fail' AND to_status = 'Failed' AND from_status IN ('Preparing', 'Planning', 'Working', 'Checking', 'Needs approval'))
  OR (input = 'pass' AND from_status = 'Checking' AND to_status = 'Done')
  OR (input = 'pause' AND from_status = 'Working' AND to_status = 'Needs approval')
  OR (input IN ('resume', 'approve', 'deny') AND from_status = 'Needs approval' AND to_status = 'Working')
);

CREATE TABLE tasks.approvals (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_id uuid NOT NULL REFERENCES tasks.tasks (id),
  user_id uuid NOT NULL,
  tool text NOT NULL,
  reason text NOT NULL,
  action text NOT NULL,
  state text NOT NULL DEFAULT 'pending',
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  CONSTRAINT approvals_tool_check CHECK (char_length(tool) BETWEEN 1 AND 64),
  CONSTRAINT approvals_reason_check CHECK (reason IN ('ask', 'expired')),
  CONSTRAINT approvals_action_check CHECK (char_length(action) BETWEEN 1 AND 200),
  CONSTRAINT approvals_state_check CHECK (state IN ('pending', 'approved', 'denied', 'closed')),
  CONSTRAINT approvals_decided_check CHECK ((state = 'pending') = (decided_at IS NULL))
);

CREATE UNIQUE INDEX approvals_one_pending
  ON tasks.approvals (task_id)
  WHERE state = 'pending';

CREATE INDEX approvals_user_idx ON tasks.approvals (user_id, created_at DESC);

ALTER TABLE tasks.approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks.approvals FORCE ROW LEVEL SECURITY;

-- The task owner is the one who decides, so a person who can only view or
-- edit a shared task never sees its approvals.
CREATE POLICY approvals_select ON tasks.approvals
  FOR SELECT
  USING (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

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
  USING (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  )
  WITH CHECK (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'task:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

-- Ending a task is not reserved to its owner: a shared editor can fail it,
-- and a parent cancel fails children owned by someone else. Whoever ends it,
-- a pending approval of a task that reached Done or Failed may be closed and
-- nothing else. The task row is read under its own row security, so this
-- only opens within tasks the actor can see.
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

-- The close picks its rows by task_id and state, so the UPDATE also needs
-- them visible through a SELECT policy, and Postgres checks that policy
-- against the new row too: the new state is 'closed', so a closed row must
-- stay visible here or the close itself is refused. Otherwise this one shows
-- exactly what approvals_close may settle, and only to people who may edit
-- the task: the close lands in the same transaction as the step that ended
-- the task, so a committed task is never both terminal and still waiting.
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
GRANT UPDATE (state, decided_at) ON tasks.approvals TO upmo_app;

-- +goose Down

DROP TRIGGER IF EXISTS approvals_decide_once ON tasks.approvals;
DROP FUNCTION IF EXISTS tasks.decide_once();
DROP TABLE IF EXISTS tasks.approvals;

-- Events already written as approve or deny stay, so the old rule is not
-- checked against them.
ALTER TABLE tasks.task_events DROP CONSTRAINT task_events_edge_check;
ALTER TABLE tasks.task_events ADD CONSTRAINT task_events_edge_check CHECK (
  (input = 'open' AND from_status = '' AND to_status = 'Preparing' AND seq = 1)
  OR (input = 'advance' AND (
    (from_status = 'Preparing' AND to_status = 'Working')
    OR (from_status = 'Planning' AND to_status = 'Working')
    OR (from_status = 'Working' AND to_status = 'Checking')
  ))
  OR (input = 'plan' AND from_status = 'Preparing' AND to_status = 'Planning')
  OR (input = 'fail' AND to_status = 'Failed' AND from_status IN ('Preparing', 'Planning', 'Working', 'Checking', 'Needs approval'))
  OR (input = 'pass' AND from_status = 'Checking' AND to_status = 'Done')
  OR (input = 'pause' AND from_status = 'Working' AND to_status = 'Needs approval')
  OR (input = 'resume' AND from_status = 'Needs approval' AND to_status = 'Working')
) NOT VALID;
