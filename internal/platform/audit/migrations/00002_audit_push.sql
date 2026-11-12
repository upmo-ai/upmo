-- Where the audit push stopped: the highest position each target accepted.
-- A missing row means position 0. Only the owner role touches this table; the
-- request role is granted nothing on it.

-- +goose Up

CREATE TABLE audit.push_cursor (
  target text PRIMARY KEY CHECK (target IN ('webhook', 'syslog')),
  position bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE IF EXISTS audit.push_cursor;
