-- +goose Up

GRANT USAGE ON SCHEMA notices TO upmo_app;

CREATE TABLE notices.notifications (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL,
  kind text NOT NULL,
  title text NOT NULL,
  task_id uuid NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  dismissed_at timestamptz NULL,
  CONSTRAINT notifications_kind_check CHECK (kind IN ('proposal', 'question', 'finished', 'failed')),
  CONSTRAINT notifications_title_check CHECK (title <> ''),
  CONSTRAINT notifications_proposal_task_check CHECK (kind <> 'proposal' OR task_id IS NULL)
);

CREATE INDEX notifications_user_created ON notices.notifications (user_id, created_at DESC);
-- The list reads only what is not dismissed.
CREATE INDEX notifications_user_live
  ON notices.notifications (user_id, created_at DESC)
  WHERE dismissed_at IS NULL;

ALTER TABLE notices.notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notices.notifications FORCE ROW LEVEL SECURITY;

CREATE POLICY notifications_own ON notices.notifications
  USING (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  WITH CHECK (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

GRANT SELECT, INSERT ON notices.notifications TO upmo_app;
-- A person dismisses a notification and changes nothing else about it. The
-- own-rows policy still decides which rows that is.
GRANT UPDATE (dismissed_at) ON notices.notifications TO upmo_app;
REVOKE ALL ON notices.goose_db_version FROM upmo_app;

-- The gateway tells settings managers about an unpriced model. It may write a
-- notice only for a person identity.settings_manager_ids() returns, and reads
-- none back.
-- +goose StatementBegin
CREATE FUNCTION notices.insert_for_manager(target uuid, kind text, title text)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  v_id uuid;
  v_prev text := current_setting('upmo.user_id', true);
BEGIN
  IF target IS NULL OR NOT EXISTS (
    SELECT 1 FROM identity.settings_manager_ids() AS m(id) WHERE m.id = target
  ) THEN
    RAISE EXCEPTION 'notices: not a settings manager' USING ERRCODE = '42501';
  END IF;
  PERFORM set_config('upmo.user_id', target::text, true);
  INSERT INTO notices.notifications (user_id, kind, title)
  VALUES (target, insert_for_manager.kind, insert_for_manager.title)
  RETURNING id INTO v_id;
  PERFORM set_config('upmo.user_id', COALESCE(v_prev, ''), true);
  RETURN v_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION notices.insert_for_manager(uuid, text, text) FROM PUBLIC;
GRANT USAGE ON SCHEMA notices TO upmo_gateway;
GRANT EXECUTE ON FUNCTION notices.insert_for_manager(uuid, text, text) TO upmo_gateway;

-- +goose Down

DROP FUNCTION IF EXISTS notices.insert_for_manager(uuid, text, text);
DROP TABLE IF EXISTS notices.notifications;
REVOKE USAGE ON SCHEMA notices FROM upmo_app;
