-- +goose Up

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

-- +goose Down

DROP POLICY IF EXISTS automations_owner_exists ON tasks.automations;
DROP FUNCTION IF EXISTS tasks.automation_exists(uuid);
DROP POLICY IF EXISTS tasks_owner_open_children ON tasks.tasks;
