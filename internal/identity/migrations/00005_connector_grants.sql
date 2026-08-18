-- +goose Up

CREATE TABLE identity.connector_grants (
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  connector text NOT NULL,
  resource_scope text NOT NULL,
  resource_ids text[] NOT NULL DEFAULT '{}',
  changes text NOT NULL,
  PRIMARY KEY (user_id, connector),
  CONSTRAINT connector_grants_scope_check CHECK (resource_scope IN ('all', 'these')),
  CONSTRAINT connector_grants_changes_check CHECK (changes IN ('allow', 'ask', 'block')),
  CONSTRAINT connector_grants_ids_check CHECK (
    (resource_scope = 'all' AND resource_ids = '{}')
    OR (resource_scope = 'these' AND resource_ids <> '{}')
  )
);

-- +goose Down

DROP TABLE IF EXISTS identity.connector_grants;
