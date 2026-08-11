-- +goose Up

CREATE TABLE identity.api_tokens (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  name text NOT NULL,
  token_hash bytea NOT NULL UNIQUE,
  scope text[] NOT NULL,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  last_used_at timestamptz,
  CONSTRAINT api_tokens_expiry_check CHECK (expires_at > created_at)
);

-- +goose Down

DROP TABLE IF EXISTS identity.api_tokens;
