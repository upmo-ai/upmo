-- +goose Up

CREATE TABLE gateway.provider_accounts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  name text NOT NULL,
  adapter text NOT NULL,
  credential_ref text NOT NULL,
  plan_kind text NOT NULL,
  house boolean NOT NULL,
  base_url text NOT NULL DEFAULT '',
  CONSTRAINT provider_accounts_adapter_check CHECK (adapter <> ''),
  CONSTRAINT provider_accounts_ref_check CHECK (credential_ref <> ''),
  CONSTRAINT provider_accounts_plan_check CHECK (plan_kind IN ('metered', 'subscription', 'local'))
);

CREATE UNIQUE INDEX provider_accounts_name ON gateway.provider_accounts (name);

CREATE TABLE gateway.routes (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  name text NOT NULL,
  capability text NOT NULL,
  decision text NOT NULL DEFAULT '',
  CONSTRAINT routes_capability_check CHECK (capability IN ('chat', 'stream', 'embed', 'decide')),
  CONSTRAINT routes_decision_check CHECK (
    (capability = 'decide' AND decision IN ('choice', 'yesno'))
    OR (capability <> 'decide' AND decision = '')
  )
);

CREATE UNIQUE INDEX routes_name ON gateway.routes (name);

CREATE TABLE gateway.route_links (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  route_id uuid NOT NULL REFERENCES gateway.routes (id) ON DELETE CASCADE,
  position int NOT NULL,
  provider_account_id uuid NOT NULL REFERENCES gateway.provider_accounts (id),
  model text NOT NULL,
  weight int NOT NULL DEFAULT 1,
  retry_budget int NOT NULL DEFAULT 1,
  required_capabilities text[] NOT NULL DEFAULT '{}',
  CONSTRAINT route_links_position_check CHECK (position > 0),
  CONSTRAINT route_links_model_check CHECK (model <> ''),
  CONSTRAINT route_links_weight_check CHECK (weight > 0),
  CONSTRAINT route_links_retry_check CHECK (retry_budget >= 0)
);

CREATE UNIQUE INDEX route_links_position ON gateway.route_links (route_id, position);

CREATE TABLE gateway.prices (
  model text PRIMARY KEY,
  version text NOT NULL,
  input_usd_per_mtok numeric,
  output_usd_per_mtok numeric,
  cache_read_usd_per_mtok numeric,
  cache_write_usd_per_mtok numeric,
  CONSTRAINT prices_version_check CHECK (version <> '')
);

CREATE TABLE gateway.ledger (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  at timestamptz NOT NULL DEFAULT now(),
  route text NOT NULL,
  link_position int NOT NULL,
  provider_account_id uuid,
  model text NOT NULL,
  plan_kind text NOT NULL,
  failure_class text NOT NULL,
  user_id uuid,
  agent_id uuid,
  run_id uuid,
  parent_run_id uuid,
  input_tokens int NOT NULL DEFAULT 0,
  output_tokens int NOT NULL DEFAULT 0,
  cache_read int NOT NULL DEFAULT 0,
  cache_write int NOT NULL DEFAULT 0,
  reasoning int NOT NULL DEFAULT 0,
  usage_arrived boolean NOT NULL DEFAULT false,
  cost_usd numeric,
  price_version text,
  CONSTRAINT ledger_route_check CHECK (route <> ''),
  CONSTRAINT ledger_model_check CHECK (model <> ''),
  CONSTRAINT ledger_plan_check CHECK (plan_kind IN ('metered', 'subscription', 'local')),
  CONSTRAINT ledger_position_check CHECK (link_position > 0),
  CONSTRAINT ledger_tokens_nonneg CHECK (
    input_tokens >= 0 AND output_tokens >= 0 AND cache_read >= 0
    AND cache_write >= 0 AND reasoning >= 0
  ),
  CONSTRAINT ledger_cost_unknown CHECK (cost_usd IS NULL OR cost_usd >= 0)
);

CREATE INDEX ledger_run ON gateway.ledger (run_id);
CREATE INDEX ledger_parent ON gateway.ledger (parent_run_id);
CREATE INDEX ledger_at ON gateway.ledger (at);

CREATE VIEW gateway.task_tokens AS
SELECT coalesce(parent_run_id, run_id) AS task_id,
  sum(input_tokens + output_tokens + cache_read + cache_write + reasoning)::bigint AS tokens
FROM gateway.ledger
WHERE run_id IS NOT NULL
GROUP BY 1;

-- +goose StatementBegin
CREATE FUNCTION gateway.cache_hit_alert(input_tokens int, cache_read int)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
AS $$
  SELECT CASE
    WHEN (input_tokens + cache_read) <= 0 THEN false
    ELSE (cache_read::numeric / (input_tokens + cache_read)) < 0.7
  END;
$$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS gateway.cache_hit_alert(int, int);
DROP VIEW IF EXISTS gateway.task_tokens;
DROP TABLE IF EXISTS gateway.ledger;
DROP TABLE IF EXISTS gateway.prices;
DROP TABLE IF EXISTS gateway.route_links;
DROP TABLE IF EXISTS gateway.routes;
DROP TABLE IF EXISTS gateway.provider_accounts;
