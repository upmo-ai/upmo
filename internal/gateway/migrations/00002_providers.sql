-- +goose Up

-- Provider accounts a person adds themselves. A row with no owner is a global
-- (house or workspace) account and stays exactly as it was. A key a person
-- adds is sealed in gateway.credentials and never leaves the database as
-- plain text; the account only holds the credential_ref that names it.

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

ALTER TABLE gateway.provider_accounts
  ADD COLUMN owner_user_id uuid,
  ADD COLUMN provider text NOT NULL DEFAULT '',
  ADD COLUMN status text NOT NULL DEFAULT 'Not connected',
  ADD COLUMN checked_at timestamptz,
  ADD COLUMN enabled boolean NOT NULL DEFAULT true,
  ADD COLUMN key_hint text NOT NULL DEFAULT '',
  ADD COLUMN key_changed_at timestamptz,
  ADD COLUMN key_changed_by uuid,
  ADD CONSTRAINT provider_accounts_status_check CHECK (
    status IN ('Not connected', 'Connected', 'Rate limited', 'Key rejected', 'Offline')
  ),
  ADD CONSTRAINT provider_accounts_house_owner_check CHECK (NOT house OR owner_user_id IS NULL),
  ADD CONSTRAINT provider_accounts_provider_check CHECK (char_length(provider) <= 60),
  ADD CONSTRAINT provider_accounts_key_hint_check CHECK (char_length(key_hint) <= 4);

-- A name is unique per owner. All global accounts count as one owner.
DROP INDEX gateway.provider_accounts_name;
CREATE UNIQUE INDEX provider_accounts_owner_name
  ON gateway.provider_accounts (COALESCE(owner_user_id, '00000000-0000-0000-0000-000000000000'::uuid), name);

-- Each account a person adds has its own credential ref.
CREATE UNIQUE INDEX provider_accounts_own_ref
  ON gateway.provider_accounts (credential_ref)
  WHERE owner_user_id IS NOT NULL;

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

-- True for the role that owns the gateway tables. That is the gateway process
-- and the migrator, which read and write accounts for every person when a
-- model call needs a key. Brain runs requests as upmo_app, never as the
-- owner, so a person's request never reaches this branch. It only reads
-- current_user, which parallel workers share with the leader, and pg_class,
-- so it is parallel safe and lets reads under these policies use workers.
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

ALTER TABLE gateway.provider_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.provider_accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.provider_models ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.provider_models FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.credentials FORCE ROW LEVEL SECURITY;

CREATE POLICY provider_accounts_service ON gateway.provider_accounts
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

-- A person sees their own accounts and the global ones, and writes only their own.
CREATE POLICY provider_accounts_select ON gateway.provider_accounts
  FOR SELECT
  USING (
    owner_user_id IS NULL
    OR owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  );

CREATE POLICY provider_accounts_insert ON gateway.provider_accounts
  FOR INSERT
  WITH CHECK (
    owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND NOT house
  );

CREATE POLICY provider_accounts_update ON gateway.provider_accounts
  FOR UPDATE
  USING (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
  WITH CHECK (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid AND NOT house);

CREATE POLICY provider_accounts_delete ON gateway.provider_accounts
  FOR DELETE
  USING (owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid);

CREATE POLICY provider_models_service ON gateway.provider_models
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

-- A model list follows its account: visible where the account is, writable
-- where the account is the person's own.
CREATE POLICY provider_models_select ON gateway.provider_models
  FOR SELECT
  USING (EXISTS (SELECT 1 FROM gateway.provider_accounts AS a WHERE a.id = account_id));

CREATE POLICY provider_models_insert ON gateway.provider_models
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND a.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY provider_models_delete ON gateway.provider_models
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.id = account_id
      AND a.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

CREATE POLICY credentials_service ON gateway.credentials
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

-- A sealed key is reachable only through an account the person owns. A global
-- account's key is never visible to a person.
CREATE POLICY credentials_own ON gateway.credentials
  FOR ALL
  USING (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref
      AND a.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM gateway.provider_accounts AS a
    WHERE a.credential_ref = ref
      AND a.owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  ));

GRANT USAGE ON SCHEMA gateway TO upmo_app;
GRANT SELECT, INSERT, DELETE ON gateway.provider_accounts TO upmo_app;
GRANT UPDATE (name, plan_kind, base_url, status, checked_at, enabled, key_hint, key_changed_at, key_changed_by) ON gateway.provider_accounts TO upmo_app;
GRANT SELECT, INSERT, DELETE ON gateway.provider_models TO upmo_app;
GRANT SELECT, INSERT, DELETE ON gateway.credentials TO upmo_app;
GRANT UPDATE (ciphertext, nonce, key_id, created_at) ON gateway.credentials TO upmo_app;
-- Names of the routes that use an account, for the remove check.
GRANT SELECT ON gateway.routes, gateway.route_links TO upmo_app;

-- +goose Down

REVOKE SELECT ON gateway.routes, gateway.route_links FROM upmo_app;
DROP POLICY IF EXISTS credentials_own ON gateway.credentials;
DROP POLICY IF EXISTS credentials_service ON gateway.credentials;
DROP POLICY IF EXISTS provider_models_delete ON gateway.provider_models;
DROP POLICY IF EXISTS provider_models_insert ON gateway.provider_models;
DROP POLICY IF EXISTS provider_models_select ON gateway.provider_models;
DROP POLICY IF EXISTS provider_models_service ON gateway.provider_models;
DROP POLICY IF EXISTS provider_accounts_delete ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_update ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_insert ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_select ON gateway.provider_accounts;
DROP POLICY IF EXISTS provider_accounts_service ON gateway.provider_accounts;
ALTER TABLE gateway.provider_accounts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.provider_accounts DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS gateway.credentials;
DROP TABLE IF EXISTS gateway.provider_models;
DROP FUNCTION IF EXISTS gateway.serving();
DROP INDEX IF EXISTS gateway.provider_accounts_own_ref;
DROP INDEX IF EXISTS gateway.provider_accounts_owner_name;
DELETE FROM gateway.route_links
WHERE provider_account_id IN (SELECT id FROM gateway.provider_accounts WHERE owner_user_id IS NOT NULL);
DELETE FROM gateway.provider_accounts WHERE owner_user_id IS NOT NULL;
CREATE UNIQUE INDEX provider_accounts_name ON gateway.provider_accounts (name);
ALTER TABLE gateway.provider_accounts
  DROP CONSTRAINT IF EXISTS provider_accounts_provider_check,
  DROP CONSTRAINT IF EXISTS provider_accounts_key_hint_check,
  DROP CONSTRAINT IF EXISTS provider_accounts_house_owner_check,
  DROP CONSTRAINT IF EXISTS provider_accounts_status_check,
  DROP COLUMN key_changed_by,
  DROP COLUMN key_changed_at,
  DROP COLUMN key_hint,
  DROP COLUMN enabled,
  DROP COLUMN checked_at,
  DROP COLUMN status,
  DROP COLUMN provider,
  DROP COLUMN owner_user_id;
