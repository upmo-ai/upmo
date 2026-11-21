-- +goose Up

GRANT USAGE ON SCHEMA platform TO upmo_app;

CREATE TABLE platform.outbox (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  topic text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz NULL,
  CONSTRAINT outbox_topic_check CHECK (topic <> '')
);

CREATE INDEX outbox_created_at ON platform.outbox (created_at);
CREATE INDEX outbox_unprocessed_created_at
  ON platform.outbox (created_at)
  WHERE processed_at IS NULL;

GRANT SELECT, INSERT, UPDATE ON platform.outbox TO upmo_app;
REVOKE ALL ON platform.goose_db_version FROM upmo_app;

-- +goose Down

DROP TABLE IF EXISTS platform.outbox;
REVOKE USAGE ON SCHEMA platform FROM upmo_app;
