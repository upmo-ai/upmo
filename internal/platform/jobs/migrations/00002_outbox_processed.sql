-- +goose Up

ALTER TABLE platform.outbox
  ADD COLUMN processed_at timestamptz NULL;

CREATE INDEX outbox_unprocessed_created_at
  ON platform.outbox (created_at)
  WHERE processed_at IS NULL;

GRANT UPDATE ON platform.outbox TO upmo_app;

-- +goose Down

REVOKE UPDATE ON platform.outbox FROM upmo_app;
DROP INDEX IF EXISTS platform.outbox_unprocessed_created_at;
ALTER TABLE platform.outbox DROP COLUMN IF EXISTS processed_at;
