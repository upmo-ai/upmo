-- +goose Up

CREATE TABLE gateway.provider_accounts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  name text NOT NULL,
  adapter text NOT NULL,
  credential_ref text NOT NULL,
  plan_kind text NOT NULL,
  house boolean NOT NULL,
  base_url text NOT NULL DEFAULT '',
  notified_causes text[] NOT NULL DEFAULT '{}',
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

CREATE TABLE gateway.tool_bundles (
  name text PRIMARY KEY,
  CONSTRAINT tool_bundles_name_check CHECK (name <> '')
);

CREATE TABLE gateway.tool_bundle_tools (
  bundle text NOT NULL REFERENCES gateway.tool_bundles (name) ON DELETE CASCADE,
  tool_name text NOT NULL,
  position int NOT NULL,
  PRIMARY KEY (bundle, tool_name),
  CONSTRAINT tool_bundle_tools_name_check CHECK (tool_name <> ''),
  CONSTRAINT tool_bundle_tools_position_check CHECK (position > 0)
);

CREATE UNIQUE INDEX tool_bundle_tools_position ON gateway.tool_bundle_tools (bundle, position);

CREATE TABLE gateway.agent_bundles (
  agent_id uuid PRIMARY KEY,
  bundle text NOT NULL REFERENCES gateway.tool_bundles (name)
);

-- vendor is the picker vendor key (e.g. 'anthropic', 'openai'). An empty
-- vendor ('') is the model-only fallback row used for vendors models.dev
-- does not know.
CREATE TABLE gateway.prices (
  vendor text NOT NULL DEFAULT '',
  model text NOT NULL,
  version text NOT NULL,
  kind text NOT NULL DEFAULT 'chat',
  inputs text[] NOT NULL DEFAULT '{text}',
  outputs text[] NOT NULL DEFAULT '{text}',
  input_usd_per_mtok numeric,
  output_usd_per_mtok numeric,
  cache_read_usd_per_mtok numeric,
  cache_write_usd_per_mtok numeric,
  max_output int,
  reasoning boolean NOT NULL DEFAULT false,
  lifecycle text NOT NULL DEFAULT '',
  retires_at date,
  capabilities text[] NOT NULL DEFAULT '{}',
  PRIMARY KEY (vendor, model),
  CONSTRAINT prices_model_check CHECK (model <> ''),
  CONSTRAINT prices_version_check CHECK (version <> ''),
  CONSTRAINT prices_kind_check CHECK (kind IN ('chat', 'embedding', 'image', 'rerank')),
  CONSTRAINT prices_inputs_check CHECK (inputs <@ ARRAY['text', 'image', 'audio', 'file']::text[]),
  CONSTRAINT prices_outputs_check CHECK (outputs <@ ARRAY['text', 'image', 'audio']::text[]),
  CONSTRAINT prices_max_output_check CHECK (max_output IS NULL OR max_output > 0),
  CONSTRAINT prices_lifecycle_check CHECK (lifecycle IN ('', 'active', 'alpha', 'beta', 'deprecated', 'retired'))
);

CREATE TABLE gateway.catalog_meta (
  id int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  version text NOT NULL,
  as_of timestamptz NOT NULL DEFAULT now(),
  refreshed_at timestamptz,
  last_attempt_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  stale_notified_at timestamptz,
  CONSTRAINT catalog_meta_version_check CHECK (version <> '')
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
  cost_micro_usd bigint,
  estimated boolean NOT NULL DEFAULT false,
  source text NOT NULL,
  source_id uuid,
  automation_id uuid,
  latency_ms int,
  charged_to_limit boolean NOT NULL DEFAULT false,
  price_source text,
  price_version text,
  unknown_notified_at timestamptz,
  hold_id uuid,
  CONSTRAINT ledger_route_check CHECK (route <> ''),
  CONSTRAINT ledger_model_check CHECK (model <> ''),
  CONSTRAINT ledger_plan_check CHECK (plan_kind IN ('metered', 'subscription', 'local')),
  CONSTRAINT ledger_position_check CHECK (link_position > 0),
  CONSTRAINT ledger_tokens_nonneg CHECK (
    input_tokens >= 0 AND output_tokens >= 0 AND cache_read >= 0
    AND cache_write >= 0 AND reasoning >= 0
  ),
  CONSTRAINT ledger_cost_nonneg CHECK (cost_micro_usd IS NULL OR cost_micro_usd >= 0),
  CONSTRAINT ledger_source_check CHECK (source IN ('chat', 'task', 'automation', 'knowledge', 'memory', 'system')),
  CONSTRAINT ledger_payer_check CHECK (user_id IS NOT NULL OR source = 'system'),
  CONSTRAINT ledger_latency_check CHECK (latency_ms IS NULL OR latency_ms >= 0),
  CONSTRAINT ledger_cost_plan_check CHECK (
    (plan_kind = 'metered' AND price_source IS NOT NULL AND (cost_micro_usd IS NOT NULL OR price_source = 'unknown'))
    OR (plan_kind IN ('subscription', 'local') AND cost_micro_usd IS NULL AND price_source IS NULL)
  ),
  CONSTRAINT ledger_price_source_check CHECK (
    (cost_micro_usd IS NULL AND (price_source IS NULL OR price_source = 'unknown'))
    OR (cost_micro_usd IS NOT NULL AND price_source IN ('override', 'provider', 'provider_list', 'models_dev', 'estimate', 'not_billed'))
  ),
  CONSTRAINT ledger_estimate_check CHECK (
    (estimated AND cost_micro_usd IS NOT NULL AND price_source = 'estimate')
    OR (NOT estimated AND COALESCE(price_source, '') <> 'estimate')
  ),
  CONSTRAINT ledger_automation_check CHECK (
    source <> 'automation' OR automation_id IS NOT NULL
  ),
  CONSTRAINT ledger_charged_to_limit_check CHECK (
    NOT charged_to_limit OR plan_kind = 'metered'
  ),
  CONSTRAINT ledger_unknown_notified_check CHECK (
    unknown_notified_at IS NULL OR price_source = 'unknown'
  )
);

CREATE INDEX ledger_run ON gateway.ledger (run_id);
CREATE INDEX ledger_parent ON gateway.ledger (parent_run_id);
CREATE INDEX ledger_at ON gateway.ledger (at);
CREATE INDEX ledger_user_at ON gateway.ledger (user_id, at);
CREATE UNIQUE INDEX ledger_unknown_notified_pair
  ON gateway.ledger (provider_account_id, model)
  WHERE unknown_notified_at IS NOT NULL;
CREATE UNIQUE INDEX ledger_hold_id
  ON gateway.ledger (hold_id)
  WHERE hold_id IS NOT NULL;

-- usage_daily keeps the per-day totals of the ledger in the workspace time
-- zone, so usage reads need not scan the ledger. user_id is null for system
-- rows, as in the ledger.
CREATE TABLE gateway.usage_daily (
  day date NOT NULL,
  user_id uuid,
  model text NOT NULL,
  source text NOT NULL,
  calls bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read bigint NOT NULL DEFAULT 0,
  cache_write bigint NOT NULL DEFAULT 0,
  reasoning bigint NOT NULL DEFAULT 0,
  cost_micro_usd bigint NOT NULL DEFAULT 0,
  CONSTRAINT usage_daily_model_check CHECK (model <> ''),
  CONSTRAINT usage_daily_source_check CHECK (source IN ('chat', 'task', 'automation', 'knowledge', 'memory', 'system')),
  CONSTRAINT usage_daily_payer_check CHECK (user_id IS NOT NULL OR source = 'system'),
  CONSTRAINT usage_daily_counts_nonneg CHECK (
    calls >= 0 AND input_tokens >= 0 AND output_tokens >= 0 AND cache_read >= 0
    AND cache_write >= 0 AND reasoning >= 0 AND cost_micro_usd >= 0
  ),
  CONSTRAINT usage_daily_key UNIQUE NULLS NOT DISTINCT (day, user_id, model, source)
);

CREATE INDEX usage_daily_user_day ON gateway.usage_daily (user_id, day);

CREATE TABLE gateway.usage_limits (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  kind text NOT NULL,
  subject_id uuid,
  amount_micro_usd bigint NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT usage_limits_kind_check CHECK (kind IN ('workspace', 'person_default', 'person', 'automation', 'ceiling')),
  CONSTRAINT usage_limits_subject_check CHECK (
    (kind IN ('workspace', 'person_default', 'ceiling') AND subject_id IS NULL)
    OR (kind IN ('person', 'automation') AND subject_id IS NOT NULL)
  ),
  CONSTRAINT usage_limits_amount_check CHECK (amount_micro_usd >= 0),
  CONSTRAINT usage_limits_key UNIQUE NULLS NOT DISTINCT (kind, subject_id)
);

CREATE TABLE gateway.usage_spend (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  kind text NOT NULL,
  subject_id uuid,
  period_start date NOT NULL,
  spent_micro_usd bigint NOT NULL DEFAULT 0,
  held_micro_usd bigint NOT NULL DEFAULT 0,
  CONSTRAINT usage_spend_kind_check CHECK (kind IN ('workspace', 'person', 'automation')),
  CONSTRAINT usage_spend_subject_check CHECK (
    (kind = 'workspace' AND subject_id IS NULL)
    OR (kind IN ('person', 'automation') AND subject_id IS NOT NULL)
  ),
  CONSTRAINT usage_spend_spent_check CHECK (spent_micro_usd >= 0),
  CONSTRAINT usage_spend_held_check CHECK (held_micro_usd >= 0),
  CONSTRAINT usage_spend_key UNIQUE NULLS NOT DISTINCT (kind, subject_id, period_start)
);

CREATE TABLE gateway.usage_holds (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  created_at timestamptz NOT NULL DEFAULT now()
);

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
DROP TABLE IF EXISTS gateway.usage_holds;
DROP TABLE IF EXISTS gateway.usage_spend;
DROP TABLE IF EXISTS gateway.usage_limits;
DROP TABLE IF EXISTS gateway.usage_daily;
DROP TABLE IF EXISTS gateway.ledger;
DROP TABLE IF EXISTS gateway.catalog_meta;
DROP TABLE IF EXISTS gateway.prices;
DROP TABLE IF EXISTS gateway.agent_bundles;
DROP TABLE IF EXISTS gateway.tool_bundle_tools;
DROP TABLE IF EXISTS gateway.tool_bundles;
DROP TABLE IF EXISTS gateway.route_links;
DROP TABLE IF EXISTS gateway.routes;
DROP TABLE IF EXISTS gateway.provider_accounts;
