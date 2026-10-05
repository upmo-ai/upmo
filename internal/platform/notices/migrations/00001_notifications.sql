-- +goose Up

GRANT USAGE ON SCHEMA notices TO upmo_app;

CREATE TABLE notices.notifications (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL,
  kind text NOT NULL,
  title text NOT NULL,
  task_id uuid NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT notifications_kind_check CHECK (kind IN ('proposal', 'question', 'finished', 'failed')),
  CONSTRAINT notifications_title_check CHECK (title <> ''),
  CONSTRAINT notifications_proposal_task_check CHECK (kind <> 'proposal' OR task_id IS NULL)
);

CREATE INDEX notifications_user_created ON notices.notifications (user_id, created_at DESC);

ALTER TABLE notices.notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notices.notifications FORCE ROW LEVEL SECURITY;

CREATE POLICY notifications_own ON notices.notifications
  USING (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  WITH CHECK (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

GRANT SELECT, INSERT ON notices.notifications TO upmo_app;
REVOKE ALL ON notices.goose_db_version FROM upmo_app;

-- +goose Down

DROP TABLE IF EXISTS notices.notifications;
REVOKE USAGE ON SCHEMA notices FROM upmo_app;
