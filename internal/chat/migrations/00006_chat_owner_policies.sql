-- +goose Up

-- chat.fail_reply runs for a person who can no longer write the chat, so no
-- policy that asks for their access lets it through. It runs as the owner of
-- the tables, and each policy below admits that role for the rows it reads or
-- writes and nothing else, like deltas_sweep_select. They hold for an owner
-- that is neither a superuser nor BYPASSRLS, since FORCE applies to an owner
-- too. Nothing outside the function runs as the owner.
CREATE POLICY chats_owner_lock_select ON chat.chats
  FOR SELECT
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.chats'::regclass))
  );

-- SELECT ... FOR UPDATE needs an UPDATE policy to lock the row. WITH CHECK
-- (false) lets it lock and refuses any real update.
CREATE POLICY chats_owner_lock_update ON chat.chats
  FOR UPDATE
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.chats'::regclass))
  )
  WITH CHECK (false);

CREATE POLICY lines_owner_select ON chat.lines
  FOR SELECT
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.lines'::regclass))
  );

-- The only line the owner stores is a failed reply.
CREATE POLICY lines_owner_fail ON chat.lines
  FOR INSERT
  WITH CHECK (
    role = 'assistant' AND status = 'failed'
    AND current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.lines'::regclass))
  );

CREATE POLICY deltas_owner_select ON chat.deltas
  FOR SELECT
  USING (
    kind = 'text'
    AND current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.deltas'::regclass))
  );

-- Owner-wide on purpose: a policy that also read chat.lines would need the
-- owner to hold that privilege. chat.fail_reply and chat.sweep_deltas fix
-- what they delete, the sweep by age, so nothing else runs as the owner.
CREATE POLICY deltas_owner_delete ON chat.deltas
  FOR DELETE
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.deltas'::regclass))
  );

-- +goose Down

DROP POLICY IF EXISTS deltas_owner_delete ON chat.deltas;
DROP POLICY IF EXISTS deltas_owner_select ON chat.deltas;
DROP POLICY IF EXISTS lines_owner_fail ON chat.lines;
DROP POLICY IF EXISTS lines_owner_select ON chat.lines;
DROP POLICY IF EXISTS chats_owner_lock_update ON chat.chats;
DROP POLICY IF EXISTS chats_owner_lock_select ON chat.chats;
