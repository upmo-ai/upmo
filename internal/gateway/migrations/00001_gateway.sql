-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
  CREATE ROLE upmo_app NOLOGIN;
EXCEPTION
  WHEN duplicate_object THEN NULL;
  WHEN unique_violation THEN NULL;
END
$$;
-- +goose StatementEnd

CREATE TABLE gateway.provider_accounts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  owner_user_id uuid,
  visibility text NOT NULL DEFAULT 'private',
  team_ids uuid[] NOT NULL DEFAULT '{}',
  name text NOT NULL,
  provider text NOT NULL DEFAULT '',
  adapter text NOT NULL,
  credential_ref text NOT NULL,
  plan_kind text NOT NULL,
  house boolean NOT NULL,
  base_url text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'Not connected',
  checked_at timestamptz,
  enabled boolean NOT NULL DEFAULT true,
  key_hint text NOT NULL DEFAULT '',
  key_changed_at timestamptz,
  key_changed_by uuid,
  notified_causes text[] NOT NULL DEFAULT '{}',
  pricing_provider text NOT NULL DEFAULT '',
  pricing_models jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT provider_accounts_adapter_check CHECK (adapter <> ''),
  CONSTRAINT provider_accounts_ref_check CHECK (credential_ref <> ''),
  CONSTRAINT provider_accounts_plan_check CHECK (plan_kind IN ('metered', 'subscription', 'local')),
  CONSTRAINT provider_accounts_status_check CHECK (
    status IN ('Not connected', 'Connected', 'Rate limited', 'Key rejected', 'Offline')
  ),
  CONSTRAINT provider_accounts_provider_check CHECK (char_length(provider) <= 60),
  CONSTRAINT provider_accounts_key_hint_check CHECK (char_length(key_hint) <= 4),
  CONSTRAINT provider_accounts_pricing_provider_check
    CHECK (pricing_provider = '' OR pricing_provider ~ '^[a-z0-9][a-z0-9_-]*$'),
  CONSTRAINT provider_accounts_pricing_models_check
    CHECK (
      jsonb_typeof(pricing_models) = 'object'
      AND octet_length(pricing_models::text) <= 65536
      AND NOT jsonb_path_exists(pricing_models, 'strict $.* ? (@.type() != "string")')
      AND jsonb_array_length(jsonb_path_query_array(pricing_models, '$.*')) <= 2000
    ),
  CONSTRAINT provider_accounts_visibility_check CHECK (visibility IN ('private', 'team', 'everyone')),
  CONSTRAINT provider_accounts_team_ids_check CHECK ((visibility = 'team') = (cardinality(team_ids) > 0)),
  CONSTRAINT provider_accounts_house_owner_check CHECK (house = (owner_user_id IS NULL)),
  CONSTRAINT provider_accounts_house_visibility_check CHECK (NOT house OR visibility = 'everyone')
);

CREATE UNIQUE INDEX provider_accounts_owner_name_private
  ON gateway.provider_accounts (owner_user_id, name)
  WHERE visibility = 'private';
CREATE UNIQUE INDEX provider_accounts_owner_name_shared
  ON gateway.provider_accounts (name)
  WHERE visibility <> 'private';
CREATE UNIQUE INDEX provider_accounts_ref
  ON gateway.provider_accounts (credential_ref)
  WHERE NOT house;
CREATE INDEX provider_accounts_team_ids ON gateway.provider_accounts USING gin (team_ids);

CREATE TABLE gateway.provider_models (
  account_id uuid NOT NULL REFERENCES gateway.provider_accounts (id) ON DELETE CASCADE,
  model text NOT NULL,
  position int NOT NULL,
  listed boolean NOT NULL DEFAULT true,
  misses int NOT NULL DEFAULT 0,
  retired boolean NOT NULL DEFAULT false,
  PRIMARY KEY (account_id, model),
  CONSTRAINT provider_models_model_check CHECK (char_length(model) BETWEEN 1 AND 120),
  CONSTRAINT provider_models_position_check CHECK (position > 0),
  CONSTRAINT provider_models_misses_check CHECK (misses >= 0)
);

CREATE UNIQUE INDEX provider_models_position ON gateway.provider_models (account_id, position);

CREATE TABLE gateway.credentials (
  ref text PRIMARY KEY,
  ciphertext bytea NOT NULL,
  nonce bytea NOT NULL,
  key_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT credentials_ref_check CHECK (ref ~ '^[A-Za-z0-9_]+$'),
  CONSTRAINT credentials_nonce_check CHECK (octet_length(nonce) = 12),
  CONSTRAINT credentials_key_id_check CHECK (key_id ~ '^[A-Za-z0-9_]{1,32}$')
);

-- Discovered models from the latest successful probe of a provider account.
-- At most 2,000 rows are stored per account (lists beyond that are refused).
CREATE TABLE gateway.account_models_seen (
  account_id uuid NOT NULL REFERENCES gateway.provider_accounts (id) ON DELETE CASCADE,
  model_id text NOT NULL,
  position int NOT NULL,
  display_name text NOT NULL DEFAULT '',
  created text NOT NULL DEFAULT '',
  context_length int,
  pricing jsonb,
  modalities jsonb,
  seen_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, model_id),
  CONSTRAINT account_models_seen_model_id_check CHECK (char_length(model_id) BETWEEN 1 AND 120),
  CONSTRAINT account_models_seen_position_check CHECK (position > 0 AND position <= 2000),
  CONSTRAINT account_models_seen_context_length_check CHECK (context_length IS NULL OR context_length >= 0)
);

CREATE UNIQUE INDEX account_models_seen_position ON gateway.account_models_seen (account_id, position);

-- Per-account, per-model price overrides (in integer micro-USD per million
-- tokens). Survives daily catalog refreshes; only settings:manage users may
-- insert, update, or delete rows, even on private accounts.
CREATE TABLE gateway.price_overrides (
  account_id uuid NOT NULL REFERENCES gateway.provider_accounts (id) ON DELETE CASCADE,
  model text NOT NULL,
  input_micro_usd_per_mtok bigint CHECK (input_micro_usd_per_mtok IS NULL OR (input_micro_usd_per_mtok >= 0 AND input_micro_usd_per_mtok <= 10000000000)),
  output_micro_usd_per_mtok bigint CHECK (output_micro_usd_per_mtok IS NULL OR (output_micro_usd_per_mtok >= 0 AND output_micro_usd_per_mtok <= 10000000000)),
  cache_read_micro_usd_per_mtok bigint CHECK (cache_read_micro_usd_per_mtok IS NULL OR (cache_read_micro_usd_per_mtok >= 0 AND cache_read_micro_usd_per_mtok <= 10000000000)),
  cache_write_micro_usd_per_mtok bigint CHECK (cache_write_micro_usd_per_mtok IS NULL OR (cache_write_micro_usd_per_mtok >= 0 AND cache_write_micro_usd_per_mtok <= 10000000000)),
  updated_by uuid NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, model),
  CONSTRAINT price_overrides_model_check CHECK (char_length(model) BETWEEN 1 AND 120)
);

CREATE TABLE gateway.routes (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  owner_user_id uuid,
  visibility text NOT NULL DEFAULT 'everyone',
  team_ids uuid[] NOT NULL DEFAULT '{}',
  installed boolean NOT NULL DEFAULT false,
  name text NOT NULL,
  capability text NOT NULL,
  decision text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT routes_capability_check CHECK (capability IN ('chat', 'stream', 'embed', 'decide')),
  CONSTRAINT routes_decision_check CHECK (
    (capability = 'decide' AND decision IN ('choice', 'yesno'))
    OR (capability <> 'decide' AND decision = '')
  ),
  CONSTRAINT routes_visibility_check CHECK (visibility IN ('private', 'team', 'everyone')),
  CONSTRAINT routes_team_ids_check CHECK ((visibility = 'team') = (cardinality(team_ids) > 0)),
  CONSTRAINT routes_installed_owner_check CHECK (installed = (owner_user_id IS NULL)),
  CONSTRAINT routes_installed_visibility_check CHECK (NOT installed OR visibility = 'everyone')
);

CREATE UNIQUE INDEX routes_private_name_uidx
  ON gateway.routes (owner_user_id, name)
  WHERE visibility = 'private';
CREATE UNIQUE INDEX routes_shared_name_uidx
  ON gateway.routes (name)
  WHERE visibility <> 'private';
CREATE INDEX routes_team_ids ON gateway.routes USING gin (team_ids);

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
  context_window int NOT NULL DEFAULT 128000,
  PRIMARY KEY (vendor, model),
  CONSTRAINT prices_model_check CHECK (model <> ''),
  CONSTRAINT prices_version_check CHECK (version <> ''),
  CONSTRAINT prices_kind_check CHECK (kind IN ('chat', 'embedding', 'image', 'rerank')),
  CONSTRAINT prices_inputs_check CHECK (inputs <@ ARRAY['text', 'image', 'audio', 'file']::text[]),
  CONSTRAINT prices_outputs_check CHECK (outputs <@ ARRAY['text', 'image', 'audio']::text[]),
  CONSTRAINT prices_max_output_check CHECK (max_output IS NULL OR max_output > 0),
  CONSTRAINT prices_lifecycle_check CHECK (lifecycle IN ('', 'active', 'alpha', 'beta', 'deprecated', 'retired')),
  CONSTRAINT prices_context_window_check CHECK (context_window > 0)
);

CREATE TABLE gateway.price_tiers (
  vendor text NOT NULL DEFAULT '',
  model text NOT NULL,
  context_min_tokens int NOT NULL,
  input_usd_per_mtok numeric,
  output_usd_per_mtok numeric,
  cache_read_usd_per_mtok numeric,
  cache_write_usd_per_mtok numeric,
  PRIMARY KEY (vendor, model, context_min_tokens),
  FOREIGN KEY (vendor, model) REFERENCES gateway.prices (vendor, model),
  CONSTRAINT price_tiers_context_check CHECK (context_min_tokens > 0)
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
  owner_team_id uuid,
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
  cache_write_1h int NOT NULL DEFAULT 0,
  reasoning int NOT NULL DEFAULT 0,
  usage_arrived boolean NOT NULL DEFAULT false,
  cost_micro_usd bigint,
  list_cost_micro_usd bigint,
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
  CONSTRAINT ledger_position_check CHECK (link_position >= 0),
  CONSTRAINT ledger_tokens_nonneg CHECK (
    input_tokens >= 0 AND output_tokens >= 0 AND cache_read >= 0
    AND cache_write >= 0 AND cache_write_1h >= 0 AND cache_write_1h <= cache_write AND reasoning >= 0
  ),
  CONSTRAINT ledger_cost_nonneg CHECK (cost_micro_usd IS NULL OR cost_micro_usd >= 0),
  CONSTRAINT ledger_list_cost_nonneg CHECK (list_cost_micro_usd IS NULL OR list_cost_micro_usd >= 0),
  CONSTRAINT ledger_list_cost_plan_check CHECK (plan_kind <> 'metered' OR list_cost_micro_usd IS NULL),
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

-- usage_daily stores totals at day, payer, workspace, agent, provider account,
-- model, plan and source grain. user_id is null for system rows, as in ledger.
CREATE TABLE gateway.usage_daily (
  day date NOT NULL,
  user_id uuid,
  owner_team_id uuid,
  agent_id uuid,
  provider_account_id uuid,
  model text NOT NULL,
  plan_kind text NOT NULL,
  source text NOT NULL,
  calls bigint NOT NULL DEFAULT 0,
  errors bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read bigint NOT NULL DEFAULT 0,
  cache_write bigint NOT NULL DEFAULT 0,
  cache_write_1h bigint NOT NULL DEFAULT 0,
  reasoning bigint NOT NULL DEFAULT 0,
  unpriced_calls bigint NOT NULL DEFAULT 0,
  estimated boolean NOT NULL DEFAULT false,
  cost_micro_usd bigint,
  list_cost_micro_usd bigint,
  CONSTRAINT usage_daily_model_check CHECK (model <> ''),
  CONSTRAINT usage_daily_plan_kind_check CHECK (plan_kind IN ('metered', 'subscription', 'local')),
  CONSTRAINT usage_daily_source_check CHECK (source IN ('chat', 'task', 'automation', 'knowledge', 'memory', 'system')),
  CONSTRAINT usage_daily_payer_check CHECK (user_id IS NOT NULL OR source = 'system'),
  CONSTRAINT usage_daily_counts_check CHECK (
    calls >= 0 AND errors >= 0 AND errors <= calls AND input_tokens >= 0 AND output_tokens >= 0
    AND cache_read >= 0 AND cache_write >= 0 AND cache_write_1h >= 0
    AND cache_write_1h <= cache_write AND reasoning >= 0 AND unpriced_calls >= 0
    AND unpriced_calls <= calls
  ),
  CONSTRAINT usage_daily_money_check CHECK (
    (cost_micro_usd IS NULL OR cost_micro_usd >= 0)
    AND (list_cost_micro_usd IS NULL OR list_cost_micro_usd >= 0)
    AND (plan_kind = 'metered' OR cost_micro_usd IS NULL)
    AND (plan_kind <> 'metered' OR list_cost_micro_usd IS NULL)
  ),
  CONSTRAINT usage_daily_key UNIQUE NULLS NOT DISTINCT
    (day, user_id, owner_team_id, agent_id, provider_account_id, model, plan_kind, source)
);

CREATE INDEX usage_daily_user_day ON gateway.usage_daily (user_id, day);

-- Records which closed days usage_daily covers and in which time zone.
CREATE TABLE gateway.usage_daily_state (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  rolled_through date NOT NULL,
  time_zone text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

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

-- True for the role that owns the gateway tables.
-- +goose StatementBegin
CREATE FUNCTION gateway.serving()
RETURNS boolean
LANGUAGE sql
STABLE
PARALLEL SAFE
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT current_user = (
    SELECT pg_get_userbyid(c.relowner)
    FROM pg_class AS c
    WHERE c.oid = 'gateway.provider_accounts'::regclass
  );
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION gateway.set_provider_pricing_map(account_id uuid, pricing_vendor text, models jsonb)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
  IF NOT ('settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))) THEN
    RAISE EXCEPTION 'pricing map requires settings:manage' USING ERRCODE = '42501';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND (a.house OR identity.may_manage_provider(a.owner_user_id, a.visibility))
  ) THEN
    RAISE EXCEPTION 'pricing map requires account access' USING ERRCODE = '42501';
  END IF;
  IF jsonb_typeof(models) <> 'object' THEN
    RAISE EXCEPTION 'pricing models must be an object' USING ERRCODE = '22023';
  END IF;
  IF jsonb_path_exists(models, 'strict $.* ? (@.type() != "string")') THEN
    RAISE EXCEPTION 'pricing model values must be strings' USING ERRCODE = '22023';
  END IF;
  IF jsonb_array_length(jsonb_path_query_array(models, '$.*')) > 2000 THEN
    RAISE EXCEPTION 'pricing map exceeds its entry limit' USING ERRCODE = '23514', CONSTRAINT = 'provider_accounts_pricing_models_check';
  END IF;
  UPDATE gateway.provider_accounts
  SET pricing_provider = pricing_vendor, pricing_models = models
  WHERE id = account_id;
  RETURN FOUND;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION gateway.set_provider_pricing_map(uuid, text, jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION gateway.set_provider_pricing_map(uuid, text, jsonb) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION gateway.shared_route_name_taken(route_name text, exclude_id uuid DEFAULT NULL)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT EXISTS (
    SELECT 1 FROM gateway.routes
    WHERE name = route_name
      AND (installed OR visibility <> 'private')
      AND (exclude_id IS NULL OR id <> exclude_id)
  );
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION gateway.shared_route_name_taken(text, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION gateway.shared_route_name_taken(text, uuid) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION gateway.provider_account_used_by_shared_route(account_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT EXISTS (
    SELECT 1
    FROM gateway.route_links AS l
    JOIN gateway.routes AS r ON r.id = l.route_id
    WHERE l.provider_account_id = account_id
      AND (r.installed OR r.visibility <> 'private')
  );
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION gateway.provider_account_used_by_shared_route(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION gateway.provider_account_used_by_shared_route(uuid) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION gateway.route_has_private_account(target_route_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT EXISTS (
    SELECT 1
    FROM gateway.route_links AS l
    JOIN gateway.provider_accounts AS a ON a.id = l.provider_account_id
    WHERE l.route_id = target_route_id
      AND a.visibility = 'private'
  );
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION gateway.route_has_private_account(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION gateway.route_has_private_account(uuid) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION gateway.usage_limit_set(p_kind text, p_subject uuid, p_amount bigint)
RETURNS bigint
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_old_amount bigint;
  v_found boolean;
BEGIN
  IF NULLIF(pg_catalog.current_setting('upmo.user_id', true), '')::uuid IS NULL THEN
    RAISE EXCEPTION 'gateway: user_id is required' USING ERRCODE = '42501';
  END IF;

  IF NOT COALESCE(
    'settings:manage' = ANY (pg_catalog.string_to_array(NULLIF(pg_catalog.current_setting('upmo.allow', true), ''), ',')),
    false
  ) THEN
    RAISE EXCEPTION 'gateway: changing spend limits needs settings:manage' USING ERRCODE = '42501';
  END IF;

  IF p_kind IS NULL OR p_kind NOT IN ('workspace', 'person_default', 'person', 'automation') THEN
    RAISE EXCEPTION 'gateway: limit kind % is not allowed', COALESCE(p_kind, '<null>') USING ERRCODE = '42501';
  END IF;

  IF p_kind IN ('workspace', 'person_default') AND p_subject IS NOT NULL THEN
    RAISE EXCEPTION 'gateway: limit kind % must not have a subject_id', p_kind USING ERRCODE = '42501';
  END IF;

  IF p_kind IN ('person', 'automation') AND p_subject IS NULL THEN
    RAISE EXCEPTION 'gateway: limit kind % requires a subject_id', p_kind USING ERRCODE = '42501';
  END IF;

  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtext('gateway.usage_limits:' || p_kind || ':' || COALESCE(p_subject::text, ''))
  );

  SELECT amount_micro_usd
  INTO v_old_amount
  FROM gateway.usage_limits
  WHERE kind = p_kind
    AND subject_id IS NOT DISTINCT FROM p_subject
  FOR UPDATE;

  v_found := FOUND;

  IF NOT v_found AND p_amount IS NULL THEN
    RETURN NULL;
  END IF;

  IF v_found AND v_old_amount = p_amount THEN
    RETURN v_old_amount;
  END IF;

  IF p_amount IS NULL THEN
    DELETE FROM gateway.usage_limits
    WHERE kind = p_kind
      AND subject_id IS NOT DISTINCT FROM p_subject;
  ELSIF NOT v_found THEN
    INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd)
    VALUES (p_kind, p_subject, p_amount);
  ELSE
    UPDATE gateway.usage_limits
    SET amount_micro_usd = p_amount,
        updated_at = pg_catalog.now()
    WHERE kind = p_kind
      AND subject_id IS NOT DISTINCT FROM p_subject;
  END IF;

  RETURN v_old_amount;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION gateway.usage_limit_set(text, uuid, bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION gateway.usage_limit_set(text, uuid, bigint) TO upmo_app;

ALTER TABLE gateway.provider_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.provider_accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.provider_models ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.provider_models FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.account_models_seen ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.account_models_seen FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.price_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.price_overrides FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.routes FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.route_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.route_links FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.ledger FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_limits FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_spend ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_spend FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_holds FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_daily ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_daily FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_daily_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_daily_state FORCE ROW LEVEL SECURITY;

CREATE POLICY provider_accounts_service ON gateway.provider_accounts
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY provider_accounts_select ON gateway.provider_accounts
  FOR SELECT
  USING (
    identity.may_use(owner_user_id, visibility, team_ids)
    OR identity.may_manage(owner_user_id, visibility)
  );

CREATE POLICY provider_accounts_insert ON gateway.provider_accounts
  FOR INSERT
  WITH CHECK (
    NOT house
    AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND identity.may_manage_provider(owner_user_id, visibility)
  );

CREATE POLICY provider_accounts_update ON gateway.provider_accounts
  FOR UPDATE
  USING (NOT house AND identity.may_manage_provider(owner_user_id, visibility))
  WITH CHECK (NOT house AND identity.may_manage_provider(owner_user_id, visibility));

CREATE POLICY provider_accounts_delete ON gateway.provider_accounts
  FOR DELETE
  USING (NOT house AND identity.may_manage_provider(owner_user_id, visibility));

CREATE POLICY provider_models_service ON gateway.provider_models
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY provider_models_select ON gateway.provider_models
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
  ));

CREATE POLICY provider_models_insert ON gateway.provider_models
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND identity.may_manage_provider(a.owner_user_id, a.visibility)
  ));

CREATE POLICY provider_models_delete ON gateway.provider_models
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND identity.may_manage_provider(a.owner_user_id, a.visibility)
  ));

CREATE POLICY credentials_service ON gateway.credentials
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY credentials_manage ON gateway.credentials
  FOR ALL
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref
      AND NOT a.house
      AND identity.may_manage_provider(a.owner_user_id, a.visibility)
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref
      AND NOT a.house
      AND identity.may_manage_provider(a.owner_user_id, a.visibility)
  ));

CREATE POLICY account_models_seen_service ON gateway.account_models_seen
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY account_models_seen_select ON gateway.account_models_seen
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
  ));

CREATE POLICY account_models_seen_insert ON gateway.account_models_seen
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND identity.may_manage_provider(a.owner_user_id, a.visibility)
  ));

CREATE POLICY account_models_seen_delete ON gateway.account_models_seen
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND identity.may_manage_provider(a.owner_user_id, a.visibility)
  ));

CREATE POLICY price_overrides_service ON gateway.price_overrides
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY price_overrides_select ON gateway.price_overrides
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
  ));

CREATE POLICY price_overrides_write ON gateway.price_overrides
  FOR ALL
  USING (
    'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND EXISTS (
      SELECT 1 FROM gateway.provider_accounts AS a
      WHERE a.id = account_id
    )
  )
  WITH CHECK (
    'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND EXISTS (
      SELECT 1 FROM gateway.provider_accounts AS a
      WHERE a.id = account_id
    )
  );

CREATE POLICY routes_service ON gateway.routes
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY routes_select ON gateway.routes
  FOR SELECT
  USING (
    installed
    OR identity.may_use(owner_user_id, visibility, team_ids)
    OR identity.may_manage(owner_user_id, visibility)
  );

CREATE POLICY routes_insert ON gateway.routes
  FOR INSERT
  WITH CHECK (
    NOT installed
    AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND identity.may_manage(owner_user_id, visibility)
  );

CREATE POLICY routes_update ON gateway.routes
  FOR UPDATE
  USING (NOT installed AND identity.may_manage(owner_user_id, visibility))
  WITH CHECK (NOT installed AND identity.may_manage(owner_user_id, visibility));

CREATE POLICY routes_delete ON gateway.routes
  FOR DELETE
  USING (NOT installed AND identity.may_manage(owner_user_id, visibility));

CREATE POLICY route_links_service ON gateway.route_links
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY route_links_select ON gateway.route_links
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM gateway.routes AS r
    WHERE r.id = route_id
  ));

CREATE POLICY route_links_insert ON gateway.route_links
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.routes AS r
    WHERE r.id = route_id
      AND NOT r.installed
      AND identity.may_manage(r.owner_user_id, r.visibility)
  ));

CREATE POLICY route_links_update ON gateway.route_links
  FOR UPDATE
  USING (EXISTS (
    SELECT 1 FROM gateway.routes AS r
    WHERE r.id = route_id
      AND NOT r.installed
      AND identity.may_manage(r.owner_user_id, r.visibility)
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.routes AS r
    WHERE r.id = route_id
      AND NOT r.installed
      AND identity.may_manage(r.owner_user_id, r.visibility)
  ));

CREATE POLICY route_links_delete ON gateway.route_links
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM gateway.routes AS r
    WHERE r.id = route_id
      AND NOT r.installed
      AND identity.may_manage(r.owner_user_id, r.visibility)
  ));

CREATE POLICY ledger_service ON gateway.ledger
  FOR ALL
  USING ((SELECT gateway.serving()))
  WITH CHECK ((SELECT gateway.serving()));

CREATE POLICY ledger_select ON gateway.ledger
  FOR SELECT TO upmo_app
  USING (
    user_id = (SELECT NULLIF(current_setting('upmo.user_id', true), '')::uuid)
    OR (SELECT 'usage:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  );

CREATE POLICY usage_limits_service ON gateway.usage_limits
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY usage_limits_select ON gateway.usage_limits
  FOR SELECT TO upmo_app
  USING (
    NULLIF(current_setting('upmo.user_id', true), '')::uuid IS NOT NULL
    AND (
      kind IN ('workspace', 'person_default', 'ceiling')
      OR (kind = 'person' AND subject_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
      OR 'usage:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    )
  );

CREATE POLICY usage_spend_service ON gateway.usage_spend
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY usage_spend_select ON gateway.usage_spend
  FOR SELECT TO upmo_app
  USING (
    NULLIF(current_setting('upmo.user_id', true), '')::uuid IS NOT NULL
    AND (
      (kind = 'person' AND subject_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
      OR 'usage:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    )
  );

CREATE POLICY usage_holds_service ON gateway.usage_holds
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

CREATE POLICY usage_daily_service ON gateway.usage_daily
  FOR ALL
  USING ((SELECT gateway.serving()))
  WITH CHECK ((SELECT gateway.serving()));

CREATE POLICY usage_daily_select ON gateway.usage_daily
  FOR SELECT TO upmo_app
  USING (
    user_id = (SELECT NULLIF(current_setting('upmo.user_id', true), '')::uuid)
    OR (SELECT 'usage:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  );

CREATE POLICY usage_daily_state_service ON gateway.usage_daily_state
  FOR ALL
  USING ((SELECT gateway.serving()))
  WITH CHECK ((SELECT gateway.serving()));

CREATE POLICY usage_daily_state_select ON gateway.usage_daily_state
  FOR SELECT TO upmo_app
  USING ((SELECT NULLIF(current_setting('upmo.user_id', true), '')) IS NOT NULL);

GRANT USAGE ON SCHEMA gateway TO upmo_app;
GRANT SELECT, INSERT, DELETE ON gateway.provider_accounts TO upmo_app;
GRANT UPDATE (
  name, plan_kind, base_url, status, checked_at, enabled,
  key_hint, key_changed_at, key_changed_by, updated_at, visibility, team_ids
) ON gateway.provider_accounts TO upmo_app;
GRANT SELECT, INSERT, DELETE ON gateway.provider_models TO upmo_app;
GRANT SELECT, INSERT, DELETE ON gateway.credentials TO upmo_app;
GRANT UPDATE (ciphertext, nonce, key_id, created_at) ON gateway.credentials TO upmo_app;
GRANT SELECT, INSERT, DELETE ON gateway.account_models_seen TO upmo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON gateway.price_overrides TO upmo_app;
GRANT SELECT, INSERT, DELETE ON gateway.routes TO upmo_app;
GRANT UPDATE (name, capability, decision, visibility, team_ids, updated_at) ON gateway.routes TO upmo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON gateway.route_links TO upmo_app;
GRANT SELECT ON gateway.prices, gateway.price_tiers TO upmo_app;
GRANT SELECT ON gateway.ledger TO upmo_app;
GRANT SELECT (kind, subject_id, amount_micro_usd, updated_at) ON gateway.usage_limits TO upmo_app;
GRANT SELECT (kind, subject_id, period_start, spent_micro_usd, held_micro_usd) ON gateway.usage_spend TO upmo_app;
GRANT SELECT ON gateway.usage_daily TO upmo_app;
GRANT SELECT ON gateway.usage_daily_state TO upmo_app;

-- +goose Down

REVOKE ALL ON gateway.usage_daily_state FROM upmo_app;
REVOKE ALL ON gateway.usage_daily FROM upmo_app;
REVOKE ALL ON gateway.usage_spend FROM upmo_app;
REVOKE ALL ON gateway.usage_limits FROM upmo_app;
REVOKE ALL ON gateway.ledger FROM upmo_app;
REVOKE ALL ON gateway.price_tiers FROM upmo_app;
REVOKE ALL ON gateway.prices FROM upmo_app;
REVOKE ALL ON gateway.route_links FROM upmo_app;
REVOKE ALL ON gateway.routes FROM upmo_app;
REVOKE ALL ON gateway.price_overrides FROM upmo_app;
REVOKE ALL ON gateway.account_models_seen FROM upmo_app;
REVOKE ALL ON gateway.credentials FROM upmo_app;
REVOKE ALL ON gateway.provider_models FROM upmo_app;
REVOKE ALL ON gateway.provider_accounts FROM upmo_app;
REVOKE USAGE ON SCHEMA gateway FROM upmo_app;

DROP FUNCTION IF EXISTS gateway.usage_limit_set(text, uuid, bigint);
DROP FUNCTION IF EXISTS gateway.route_has_private_account(uuid);
DROP FUNCTION IF EXISTS gateway.provider_account_used_by_shared_route(uuid);
DROP FUNCTION IF EXISTS gateway.shared_route_name_taken(text, uuid);
DROP FUNCTION IF EXISTS gateway.set_provider_pricing_map(uuid, text, jsonb);

DROP TABLE IF EXISTS gateway.usage_holds;
DROP TABLE IF EXISTS gateway.usage_spend;
DROP TABLE IF EXISTS gateway.usage_limits;
DROP TABLE IF EXISTS gateway.usage_daily_state;
DROP TABLE IF EXISTS gateway.usage_daily;
DROP TABLE IF EXISTS gateway.ledger;
DROP TABLE IF EXISTS gateway.catalog_meta;
DROP TABLE IF EXISTS gateway.price_tiers;
DROP TABLE IF EXISTS gateway.prices;
DROP TABLE IF EXISTS gateway.tool_bundle_tools;
DROP TABLE IF EXISTS gateway.tool_bundles;
DROP TABLE IF EXISTS gateway.route_links;
DROP TABLE IF EXISTS gateway.routes;
DROP TABLE IF EXISTS gateway.price_overrides;
DROP TABLE IF EXISTS gateway.account_models_seen;
DROP TABLE IF EXISTS gateway.credentials;
DROP TABLE IF EXISTS gateway.provider_models;
DROP TABLE IF EXISTS gateway.provider_accounts;
DROP FUNCTION IF EXISTS gateway.serving();
