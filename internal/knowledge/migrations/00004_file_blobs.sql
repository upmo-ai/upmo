-- +goose Up

ALTER TABLE knowledge.sources
  ADD COLUMN blob_key text NOT NULL DEFAULT '';

REVOKE UPDATE ON knowledge.sources FROM upmo_app;
GRANT UPDATE (title, body, sha256, revision, updated_at, kind, source_ref, provenance, status, error, blob_key)
  ON knowledge.sources TO upmo_app;

-- +goose Down

REVOKE UPDATE ON knowledge.sources FROM upmo_app;
GRANT UPDATE (title, body, sha256, revision, updated_at, kind, source_ref, provenance, status, error)
  ON knowledge.sources TO upmo_app;

ALTER TABLE knowledge.sources
  DROP COLUMN IF EXISTS blob_key;
