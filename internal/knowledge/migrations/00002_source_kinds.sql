-- +goose Up

ALTER TABLE knowledge.sources
  ADD COLUMN kind text NOT NULL DEFAULT 'markdown',
  ADD COLUMN source_ref text NOT NULL DEFAULT '',
  ADD COLUMN provenance text NOT NULL DEFAULT 'curated',
  ADD COLUMN status text NOT NULL DEFAULT 'ready',
  ADD COLUMN error text NOT NULL DEFAULT '';

ALTER TABLE knowledge.sources
  ADD CONSTRAINT sources_kind_check CHECK (kind IN ('markdown', 'file', 'url', 'clip')),
  ADD CONSTRAINT sources_provenance_check CHECK (provenance IN ('curated', 'web')),
  ADD CONSTRAINT sources_status_check CHECK (status IN ('pending', 'ready', 'failed'));

-- Existing rows keep the column defaults (markdown / curated / ready).

CREATE UNIQUE INDEX sources_clip_ref
  ON knowledge.sources (source_ref)
  WHERE kind = 'clip' AND source_ref <> '';

REVOKE UPDATE ON knowledge.sources FROM upmo_app;
GRANT UPDATE (title, body, sha256, revision, updated_at, kind, source_ref, provenance, status, error)
  ON knowledge.sources TO upmo_app;

-- +goose Down

DROP INDEX IF EXISTS knowledge.sources_clip_ref;

ALTER TABLE knowledge.sources
  DROP CONSTRAINT IF EXISTS sources_kind_check,
  DROP CONSTRAINT IF EXISTS sources_provenance_check,
  DROP CONSTRAINT IF EXISTS sources_status_check;

ALTER TABLE knowledge.sources
  DROP COLUMN IF EXISTS kind,
  DROP COLUMN IF EXISTS source_ref,
  DROP COLUMN IF EXISTS provenance,
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS error;

REVOKE UPDATE ON knowledge.sources FROM upmo_app;
GRANT UPDATE (title, body, sha256, revision, updated_at) ON knowledge.sources TO upmo_app;
