-- +goose Up

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

CREATE INDEX chunks_embedding_hnsw
  ON knowledge.chunks USING hnsw (embedding halfvec_cosine_ops);

GRANT SELECT, INSERT ON knowledge.embed_models TO upmo_app;
GRANT EXECUTE ON FUNCTION knowledge.one_embedding_model() TO upmo_app;
REVOKE ALL ON FUNCTION knowledge.one_embedding_model() FROM PUBLIC;

-- +goose Down

DROP INDEX IF EXISTS knowledge.chunks_embedding_hnsw;
DROP TRIGGER IF EXISTS chunks_one_embedding_model ON knowledge.chunks;
DROP FUNCTION IF EXISTS knowledge.one_embedding_model();
DROP TABLE IF EXISTS knowledge.embed_models;
