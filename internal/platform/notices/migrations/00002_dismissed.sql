-- +goose Up

ALTER TABLE notices.notifications
  ADD COLUMN dismissed_at timestamptz NULL;

-- The list reads only what is not dismissed.
CREATE INDEX notifications_user_live
  ON notices.notifications (user_id, created_at DESC)
  WHERE dismissed_at IS NULL;

-- A person dismisses a notification and changes nothing else about it. The
-- own-rows policy still decides which rows that is.
GRANT UPDATE (dismissed_at) ON notices.notifications TO upmo_app;

-- +goose Down

REVOKE UPDATE (dismissed_at) ON notices.notifications FROM upmo_app;
DROP INDEX IF EXISTS notices.notifications_user_live;
ALTER TABLE notices.notifications DROP COLUMN IF EXISTS dismissed_at;
