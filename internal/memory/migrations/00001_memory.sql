-- +goose Up

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE memory.profiles (
  user_id uuid PRIMARY KEY,
  body text NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE memory.embed_models (
  user_id uuid PRIMARY KEY,
  model text NOT NULL,
  CONSTRAINT embed_models_model_check CHECK (model <> '')
);

CREATE TABLE memory.facts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL,
  trust text NOT NULL,
  claim text NOT NULL,
  claim_hash bytea NOT NULL,
  valid_from timestamptz NOT NULL,
  invalid_at timestamptz,
  superseded_by uuid,
  last_confirmed_at timestamptz NOT NULL,
  embedding halfvec(1024),
  embedding_model text NOT NULL DEFAULT '',
  source_kind text NOT NULL,
  source_id uuid NOT NULL,
  source_seq int NOT NULL,
  created_at timestamptz NOT NULL,
  CONSTRAINT facts_trust_check CHECK (trust IN ('user', 'admin', 'tool', 'web', 'document')),
  CONSTRAINT facts_claim_check CHECK (claim <> ''),
  CONSTRAINT facts_hash_check CHECK (octet_length(claim_hash) = 32),
  CONSTRAINT facts_source_check CHECK (source_kind <> '' AND source_seq >= 0),
  CONSTRAINT facts_span_check CHECK (invalid_at IS NULL OR invalid_at >= valid_from),
  CONSTRAINT facts_supersede_check CHECK (
    superseded_by IS NULL
    OR (superseded_by <> id AND invalid_at IS NOT NULL)
  ),
  CONSTRAINT facts_embed_pair_check CHECK (
    (embedding IS NULL AND embedding_model = '')
    OR (embedding IS NOT NULL AND embedding_model <> '')
  ),
  CONSTRAINT facts_superseded_by_fk FOREIGN KEY (superseded_by) REFERENCES memory.facts (id)
);

CREATE UNIQUE INDEX facts_active_hash ON memory.facts (user_id, claim_hash) WHERE invalid_at IS NULL;

CREATE INDEX facts_person ON memory.facts (user_id);

-- +goose StatementBegin
CREATE FUNCTION memory.one_embedding_model()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  IF NEW.embedding IS NULL THEN
    RETURN NEW;
  END IF;
  INSERT INTO memory.embed_models (user_id, model)
  VALUES (NEW.user_id, NEW.embedding_model)
  ON CONFLICT (user_id) DO NOTHING;
  IF EXISTS (
    SELECT 1
    FROM memory.embed_models
    WHERE user_id = NEW.user_id
      AND model <> NEW.embedding_model
  ) THEN
    RAISE EXCEPTION 'mixed embedding_model';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER facts_one_embedding_model
BEFORE INSERT OR UPDATE OF embedding, embedding_model ON memory.facts
FOR EACH ROW
EXECUTE FUNCTION memory.one_embedding_model();

ALTER TABLE memory.profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory.profiles FORCE ROW LEVEL SECURITY;
ALTER TABLE memory.facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory.facts FORCE ROW LEVEL SECURITY;
ALTER TABLE memory.embed_models ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory.embed_models FORCE ROW LEVEL SECURITY;

CREATE POLICY profiles_own ON memory.profiles
  USING (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  WITH CHECK (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

CREATE POLICY facts_own ON memory.facts
  USING (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  WITH CHECK (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

CREATE POLICY embed_models_own ON memory.embed_models
  USING (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  WITH CHECK (user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

GRANT USAGE ON SCHEMA memory TO upmo_app;
GRANT SELECT, INSERT ON memory.profiles TO upmo_app;
GRANT UPDATE (body, updated_at) ON memory.profiles TO upmo_app;
GRANT SELECT, INSERT ON memory.facts TO upmo_app;
GRANT UPDATE (invalid_at, superseded_by, last_confirmed_at, embedding, embedding_model) ON memory.facts TO upmo_app;
GRANT SELECT, INSERT ON memory.embed_models TO upmo_app;
GRANT EXECUTE ON FUNCTION memory.one_embedding_model() TO upmo_app;
REVOKE ALL ON FUNCTION memory.one_embedding_model() FROM PUBLIC;

-- +goose Down

DROP TABLE IF EXISTS memory.facts;
DROP TABLE IF EXISTS memory.embed_models;
DROP TABLE IF EXISTS memory.profiles;
DROP FUNCTION IF EXISTS memory.one_embedding_model();
