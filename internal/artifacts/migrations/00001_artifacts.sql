-- +goose Up

CREATE TABLE artifacts.artifacts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL DEFAULT 'artifact',
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL,
  visibility_team_id uuid,
  task_id uuid NOT NULL,
  name text NOT NULL,
  store text NOT NULL,
  object_key text NOT NULL,
  CONSTRAINT artifacts_type_check CHECK (object_type = 'artifact'),
  CONSTRAINT artifacts_visibility_check CHECK (
    (visibility IN ('private', 'everyone') AND visibility_team_id IS NULL)
    OR (visibility = 'team' AND visibility_team_id IS NOT NULL)
  ),
  CONSTRAINT artifacts_store_check CHECK (store IN ('volume', 'object')),
  CONSTRAINT artifacts_name_check CHECK (char_length(name) BETWEEN 1 AND 200),
  CONSTRAINT artifacts_key_check CHECK (
    char_length(object_key) BETWEEN 1 AND 1024
    AND object_key NOT LIKE '/%'
    AND object_key NOT LIKE '%\\%'
    AND object_key <> '..'
    AND object_key NOT LIKE '../%'
    AND object_key NOT LIKE '%/../%'
    AND object_key NOT LIKE '%/..'
  )
);

CREATE INDEX artifacts_owner ON artifacts.artifacts (owner_user_id);

SELECT identity.protect('artifacts.artifacts');

-- +goose Down

DROP TABLE IF EXISTS artifacts.artifacts;
