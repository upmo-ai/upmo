-- +goose Up

-- task_sandboxes records which sandbox each task holds, so a restarted brain
-- can still release it. A null sandbox_id is a Create still in flight. task_id
-- names a tasks.tasks row with no foreign key, because nothing crosses a
-- schema. The row is not a work object: brain reads and releases it with no
-- person's access, so like the identity tables it stays outside row security,
-- and upmo_app may only fill the sandbox and its expiry, through
-- runtime.fill_task_sandbox. proxy_secret_hash is the SHA-256 of the proxy
-- secret brain generates and sends to sandboxd on Create. Brain inserts it
-- with the pending row; upmo_app may insert it but never read or update it.
-- Sandboxd proves it holds the secret when it binds the sandbox through
-- gateway.attach_sandbox_credential.
CREATE TABLE runtime.task_sandboxes (
  task_id uuid PRIMARY KEY,
  sandbox_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  proxy_secret_hash bytea,
  CONSTRAINT task_sandboxes_task_check CHECK (task_id <> '00000000-0000-0000-0000-000000000000'),
  CONSTRAINT task_sandboxes_sandbox_check CHECK (sandbox_id IS NULL OR (sandbox_id <> '' AND sandbox_id = btrim(sandbox_id))),
  CONSTRAINT task_sandboxes_proxy_secret_hash_check CHECK (proxy_secret_hash IS NULL OR octet_length(proxy_secret_hash) = 32)
);

GRANT USAGE ON SCHEMA runtime TO upmo_app;
GRANT SELECT (task_id, sandbox_id, created_at, expires_at) ON runtime.task_sandboxes TO upmo_app;
GRANT INSERT (task_id, sandbox_id, created_at, expires_at, proxy_secret_hash) ON runtime.task_sandboxes TO upmo_app;
GRANT UPDATE (expires_at) ON runtime.task_sandboxes TO upmo_app;
GRANT DELETE ON runtime.task_sandboxes TO upmo_app;

-- Fill records the sandbox a Create returned and its expiry. It sets
-- sandbox_id only while it is null, so a bound sandbox never changes, and it
-- never touches proxy_secret_hash. A row bound to another sandbox is left
-- alone and the call returns false.
-- +goose StatementBegin
CREATE FUNCTION runtime.fill_task_sandbox(task uuid, sandbox text, expires timestamptz)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  n bigint;
BEGIN
  IF sandbox IS NULL OR sandbox !~ '^[A-Za-z0-9._-]{1,64}$' THEN
    RAISE EXCEPTION 'runtime: invalid sandbox id' USING ERRCODE = '42501';
  END IF;
  UPDATE runtime.task_sandboxes AS t
  SET expires_at = coalesce(expires, t.expires_at),
      sandbox_id = coalesce(t.sandbox_id, sandbox)
  WHERE t.task_id = task
    AND (t.sandbox_id IS NULL OR t.sandbox_id = sandbox);
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n = 1;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION runtime.fill_task_sandbox(uuid, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION runtime.fill_task_sandbox(uuid, text, timestamptz) TO upmo_app;

-- +goose Down

DROP FUNCTION IF EXISTS runtime.fill_task_sandbox(uuid, text, timestamptz);
DROP TABLE IF EXISTS runtime.task_sandboxes;
