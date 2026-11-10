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

-- +goose Down

DROP POLICY IF EXISTS tasks_owner_open_children ON tasks.tasks;
