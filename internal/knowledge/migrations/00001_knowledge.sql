-- +goose Up

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE knowledge.folders (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL DEFAULT 'folder',
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL,
  visibility_team_id uuid,
  name text NOT NULL,
  CONSTRAINT folders_type_check CHECK (object_type = 'folder'),
  CONSTRAINT folders_name_check CHECK (name <> ''),
  CONSTRAINT folders_visibility_check CHECK (
    (visibility IN ('private', 'everyone') AND visibility_team_id IS NULL)
    OR (visibility = 'team' AND visibility_team_id IS NOT NULL)
  )
);

CREATE TABLE knowledge.sources (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  folder_id uuid NOT NULL REFERENCES knowledge.folders (id) ON DELETE CASCADE,
  title text NOT NULL,
  body text NOT NULL,
  sha256 bytea NOT NULL,
  revision int NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL,
  kind text NOT NULL DEFAULT 'markdown',
  source_ref text NOT NULL DEFAULT '',
  provenance text NOT NULL DEFAULT 'curated',
  status text NOT NULL DEFAULT 'ready',
  error text NOT NULL DEFAULT '',
  blob_key text NOT NULL DEFAULT '',
  CONSTRAINT sources_title_check CHECK (title <> ''),
  CONSTRAINT sources_sha_check CHECK (octet_length(sha256) = 32),
  CONSTRAINT sources_revision_check CHECK (revision >= 0),
  CONSTRAINT sources_kind_check CHECK (kind IN ('markdown', 'file', 'url', 'clip')),
  CONSTRAINT sources_provenance_check CHECK (provenance IN ('curated', 'web')),
  CONSTRAINT sources_status_check CHECK (status IN ('pending', 'ready', 'failed'))
);

CREATE INDEX sources_folder ON knowledge.sources (folder_id);
CREATE UNIQUE INDEX sources_clip_ref
  ON knowledge.sources (source_ref)
  WHERE kind = 'clip' AND source_ref <> '';

CREATE TABLE knowledge.chunks (
  source_id uuid NOT NULL REFERENCES knowledge.sources (id) ON DELETE CASCADE,
  revision int NOT NULL,
  seq int NOT NULL,
  body text NOT NULL,
  context_prefix text NOT NULL DEFAULT '',
  trust text NOT NULL DEFAULT 'document',
  embedding halfvec(1024),
  embedding_model text NOT NULL DEFAULT '',
  body_tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', body)) STORED,
  prefix_tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', context_prefix)) STORED,
  PRIMARY KEY (source_id, revision, seq),
  CONSTRAINT chunks_body_check CHECK (body <> ''),
  CONSTRAINT chunks_trust_check CHECK (trust = 'document'),
  CONSTRAINT chunks_revision_check CHECK (revision > 0 AND seq >= 0),
  CONSTRAINT chunks_embed_pair_check CHECK (
    (embedding IS NULL AND embedding_model = '')
    OR (embedding IS NOT NULL AND embedding_model <> '')
  )
);

CREATE INDEX chunks_body_fts ON knowledge.chunks USING gin (body_tsv);
CREATE INDEX chunks_prefix_fts ON knowledge.chunks USING gin (prefix_tsv);
CREATE INDEX chunks_embedding_hnsw
  ON knowledge.chunks USING hnsw (embedding halfvec_cosine_ops);

CREATE TABLE knowledge.embed_models (
  model text PRIMARY KEY
);

-- +goose StatementBegin
CREATE FUNCTION knowledge.one_embedding_model()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.embedding IS NULL THEN
    RETURN NEW;
  END IF;
  INSERT INTO knowledge.embed_models (model)
  VALUES (NEW.embedding_model)
  ON CONFLICT (model) DO NOTHING;
  IF EXISTS (
    SELECT 1
    FROM knowledge.embed_models
    WHERE model <> NEW.embedding_model
  ) THEN
    RAISE EXCEPTION 'mixed embedding_model';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER chunks_one_embedding_model
BEFORE INSERT OR UPDATE OF embedding, embedding_model ON knowledge.chunks
FOR EACH ROW
EXECUTE FUNCTION knowledge.one_embedding_model();

ALTER TABLE knowledge.sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.sources FORCE ROW LEVEL SECURITY;
ALTER TABLE knowledge.chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.chunks FORCE ROW LEVEL SECURITY;

CREATE POLICY sources_select ON knowledge.sources
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM knowledge.folders AS folder
    WHERE folder.id = folder_id
      AND identity.may('view', folder.object_type, folder.owner_user_id, folder.visibility, folder.visibility_team_id, folder.id)
  ));

CREATE POLICY sources_insert ON knowledge.sources
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM knowledge.folders AS folder
    WHERE folder.id = folder_id
      AND identity.may('edit', folder.object_type, folder.owner_user_id, folder.visibility, folder.visibility_team_id, folder.id)
  ));

CREATE POLICY sources_update ON knowledge.sources
  FOR UPDATE
  USING (EXISTS (
    SELECT 1 FROM knowledge.folders AS folder
    WHERE folder.id = folder_id
      AND identity.may('edit', folder.object_type, folder.owner_user_id, folder.visibility, folder.visibility_team_id, folder.id)
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM knowledge.folders AS folder
    WHERE folder.id = folder_id
      AND identity.may('edit', folder.object_type, folder.owner_user_id, folder.visibility, folder.visibility_team_id, folder.id)
  ));

CREATE POLICY sources_delete ON knowledge.sources
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM knowledge.folders AS folder
    WHERE folder.id = folder_id
      AND identity.may('edit', folder.object_type, folder.owner_user_id, folder.visibility, folder.visibility_team_id, folder.id)
  ));

CREATE POLICY chunks_select ON knowledge.chunks
  FOR SELECT
  USING (EXISTS (
    SELECT 1
    FROM knowledge.sources AS source
    JOIN knowledge.folders AS folder ON folder.id = source.folder_id
    WHERE source.id = source_id
      AND identity.may('view', folder.object_type, folder.owner_user_id, folder.visibility, folder.visibility_team_id, folder.id)
  ));

CREATE POLICY chunks_insert ON knowledge.chunks
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1
    FROM knowledge.sources AS source
    JOIN knowledge.folders AS folder ON folder.id = source.folder_id
    WHERE source.id = source_id
      AND identity.may('edit', folder.object_type, folder.owner_user_id, folder.visibility, folder.visibility_team_id, folder.id)
  ));

CREATE POLICY chunks_delete ON knowledge.chunks
  FOR DELETE
  USING (EXISTS (
    SELECT 1
    FROM knowledge.sources AS source
    JOIN knowledge.folders AS folder ON folder.id = source.folder_id
    WHERE source.id = source_id
      AND identity.may('edit', folder.object_type, folder.owner_user_id, folder.visibility, folder.visibility_team_id, folder.id)
  ));

GRANT USAGE ON SCHEMA knowledge TO upmo_app;
GRANT SELECT, INSERT, DELETE ON knowledge.sources TO upmo_app;
GRANT UPDATE (title, body, sha256, revision, updated_at, kind, source_ref, provenance, status, error, blob_key)
  ON knowledge.sources TO upmo_app;
GRANT SELECT, INSERT, DELETE ON knowledge.chunks TO upmo_app;
GRANT SELECT, INSERT ON knowledge.embed_models TO upmo_app;
GRANT EXECUTE ON FUNCTION knowledge.one_embedding_model() TO upmo_app;
REVOKE ALL ON FUNCTION knowledge.one_embedding_model() FROM PUBLIC;

SELECT identity.protect('knowledge.folders');

-- +goose Down

DROP TRIGGER IF EXISTS chunks_one_embedding_model ON knowledge.chunks;
DROP FUNCTION IF EXISTS knowledge.one_embedding_model();
DROP TABLE IF EXISTS knowledge.embed_models;
DROP TABLE IF EXISTS knowledge.chunks;
DROP TABLE IF EXISTS knowledge.sources;
DROP TABLE IF EXISTS knowledge.folders;
