-- +goose Up

-- One row per accepted provision bearer: the key that verified it and the
-- nonce it carried. The primary key makes every nonce single-use per key.
CREATE TABLE identity.provision_nonces (
  key_id  text        NOT NULL,
  nonce   text        NOT NULL,
  seen_at timestamptz NOT NULL,
  PRIMARY KEY (key_id, nonce)
);

-- Replay state is control-plane auth state: only the schema owner writes it,
-- never upmo_app (the schema's default privileges would grant writes).
REVOKE INSERT, UPDATE, DELETE ON identity.provision_nonces FROM upmo_app;

-- +goose Down

DROP TABLE IF EXISTS identity.provision_nonces;
