-- +goose Up

ALTER TABLE agents.agents
  ADD COLUMN IF NOT EXISTS pack_version text NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE agents.agents
  DROP COLUMN IF EXISTS pack_version;
