-- +goose Up

-- task_sandboxes records which sandbox each task holds, so a restarted brain
-- can still release it. A null sandbox_id is a Create still in flight. task_id
-- names a tasks.tasks row with no foreign key, because nothing crosses a
-- schema. The row is not a work object: brain reads and releases it with no
-- person's access, so like the identity tables it stays outside row security,
-- and upmo_app may only fill the sandbox and its expiry.
CREATE TABLE runtime.task_sandboxes (
  task_id uuid PRIMARY KEY,
  sandbox_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  CONSTRAINT task_sandboxes_task_check CHECK (task_id <> '00000000-0000-0000-0000-000000000000'),
  CONSTRAINT task_sandboxes_sandbox_check CHECK (sandbox_id IS NULL OR (sandbox_id <> '' AND sandbox_id = btrim(sandbox_id)))
);

GRANT USAGE ON SCHEMA runtime TO upmo_app;
GRANT SELECT, INSERT, DELETE ON runtime.task_sandboxes TO upmo_app;
GRANT UPDATE (sandbox_id, expires_at) ON runtime.task_sandboxes TO upmo_app;

-- +goose Down

DROP TABLE IF EXISTS runtime.task_sandboxes;
