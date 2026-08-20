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

-- +goose Down

DROP TABLE IF EXISTS gateway.route_links;
DROP TABLE IF EXISTS gateway.routes;
DROP TABLE IF EXISTS gateway.provider_accounts;
