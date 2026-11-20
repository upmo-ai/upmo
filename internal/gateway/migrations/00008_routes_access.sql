-- Gateway routes join the shared-configuration model. Installed routes stay
-- ownerless and visible to everyone; user-created routes belong to an owner
-- and carry private, team, or everyone visibility. Row policies and the
-- gateway call the identity rules so who may use or manage a route stays
-- aligned.

-- +goose Up

ALTER TABLE gateway.routes
  ADD COLUMN owner_user_id uuid,
  ADD COLUMN visibility text NOT NULL DEFAULT 'everyone',
  ADD COLUMN team_ids uuid[] NOT NULL DEFAULT '{}',
  ADD COLUMN installed boolean NOT NULL DEFAULT false,
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

UPDATE gateway.routes
SET installed = true,
    visibility = 'everyone',
    owner_user_id = NULL,
    team_ids = '{}';

ALTER TABLE gateway.routes
  ADD CONSTRAINT routes_visibility_check CHECK (visibility IN ('private', 'team', 'everyone')),
  ADD CONSTRAINT routes_team_ids_check CHECK ((visibility = 'team') = (cardinality(team_ids) > 0)),
  ADD CONSTRAINT routes_installed_owner_check CHECK (installed = (owner_user_id IS NULL)),
  ADD CONSTRAINT routes_installed_visibility_check CHECK (NOT installed OR visibility = 'everyone');

DROP INDEX IF EXISTS gateway.routes_name;
CREATE UNIQUE INDEX routes_private_name_uidx
  ON gateway.routes (owner_user_id, name)
  WHERE visibility = 'private';
CREATE UNIQUE INDEX routes_shared_name_uidx
  ON gateway.routes (name)
  WHERE visibility <> 'private';
CREATE INDEX routes_team_ids ON gateway.routes USING gin (team_ids);

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

ALTER TABLE gateway.routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.routes FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.route_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.route_links FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.ledger FORCE ROW LEVEL SECURITY;

-- The ledger and usage_daily reach millions of rows. Each row-independent
-- check sits in a scalar sub-select so Postgres evaluates it once per
-- statement (an InitPlan) instead of once per row.
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

GRANT SELECT ON gateway.ledger TO upmo_app;
REVOKE INSERT, UPDATE, DELETE ON gateway.ledger FROM upmo_app;

ALTER TABLE gateway.usage_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_limits FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_spend ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_spend FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_holds FORCE ROW LEVEL SECURITY;

CREATE POLICY usage_limits_service ON gateway.usage_limits
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

-- A signed-in person sees workspace, person_default, ceiling (for computing
-- EffectiveWorkspaceLimit), and their own person row. usage:view sees every
-- row. upmo_app has no direct write grants or policies on usage_limits;
-- settings:manage writes go through gateway.usage_limit_set, which locks and
-- mutates non-ceiling rows and returns the previous amount.
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

REVOKE ALL ON gateway.usage_limits FROM upmo_app;
GRANT SELECT (kind, subject_id, amount_micro_usd, updated_at) ON gateway.usage_limits TO upmo_app;

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

CREATE POLICY usage_spend_service ON gateway.usage_spend
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

-- A signed-in person sees only their own spend row; usage:view sees all rows.
-- upmo_app never writes usage_spend.
CREATE POLICY usage_spend_select ON gateway.usage_spend
  FOR SELECT TO upmo_app
  USING (
    NULLIF(current_setting('upmo.user_id', true), '')::uuid IS NOT NULL
    AND (
      (kind = 'person' AND subject_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
      OR 'usage:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    )
  );

REVOKE ALL ON gateway.usage_spend FROM upmo_app;
GRANT SELECT (kind, subject_id, period_start, spent_micro_usd, held_micro_usd) ON gateway.usage_spend TO upmo_app;

CREATE POLICY usage_holds_service ON gateway.usage_holds
  FOR ALL
  USING (gateway.serving())
  WITH CHECK (gateway.serving());

REVOKE ALL ON gateway.usage_holds FROM upmo_app;

ALTER TABLE gateway.usage_daily ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_daily FORCE ROW LEVEL SECURITY;

CREATE POLICY usage_daily_service ON gateway.usage_daily
  FOR ALL
  USING ((SELECT gateway.serving()))
  WITH CHECK ((SELECT gateway.serving()));

-- A signed-in person sees only their own daily totals; usage:view sees all
-- rows. upmo_app never writes usage_daily.
CREATE POLICY usage_daily_select ON gateway.usage_daily
  FOR SELECT TO upmo_app
  USING (
    user_id = (SELECT NULLIF(current_setting('upmo.user_id', true), '')::uuid)
    OR (SELECT 'usage:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  );

REVOKE ALL ON gateway.usage_daily FROM upmo_app;
GRANT SELECT ON gateway.usage_daily TO upmo_app;

ALTER TABLE gateway.usage_daily_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_daily_state FORCE ROW LEVEL SECURITY;

CREATE POLICY usage_daily_state_service ON gateway.usage_daily_state
  FOR ALL
  USING ((SELECT gateway.serving()))
  WITH CHECK ((SELECT gateway.serving()));

CREATE POLICY usage_daily_state_select ON gateway.usage_daily_state
  FOR SELECT TO upmo_app
  USING ((SELECT NULLIF(current_setting('upmo.user_id', true), '')) IS NOT NULL);

REVOKE ALL ON gateway.usage_daily_state FROM upmo_app;
GRANT SELECT ON gateway.usage_daily_state TO upmo_app;

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

GRANT INSERT, DELETE ON gateway.routes TO upmo_app;
GRANT UPDATE (name, capability, decision, visibility, team_ids, updated_at) ON gateway.routes TO upmo_app;
GRANT INSERT, UPDATE, DELETE ON gateway.route_links TO upmo_app;

-- +goose Down

REVOKE ALL ON gateway.ledger FROM upmo_app;
REVOKE ALL ON gateway.usage_daily_state FROM upmo_app;
DROP POLICY IF EXISTS usage_daily_state_select ON gateway.usage_daily_state;
DROP POLICY IF EXISTS usage_daily_state_service ON gateway.usage_daily_state;
ALTER TABLE gateway.usage_daily_state NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_daily_state DISABLE ROW LEVEL SECURITY;
REVOKE ALL ON gateway.usage_daily FROM upmo_app;
DROP POLICY IF EXISTS usage_daily_select ON gateway.usage_daily;
DROP POLICY IF EXISTS usage_daily_service ON gateway.usage_daily;
ALTER TABLE gateway.usage_daily NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_daily DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS usage_holds_service ON gateway.usage_holds;
ALTER TABLE gateway.usage_holds NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_holds DISABLE ROW LEVEL SECURITY;
REVOKE ALL ON gateway.usage_spend FROM upmo_app;
DROP POLICY IF EXISTS usage_spend_select ON gateway.usage_spend;
DROP POLICY IF EXISTS usage_spend_service ON gateway.usage_spend;
ALTER TABLE gateway.usage_spend NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_spend DISABLE ROW LEVEL SECURITY;
DROP FUNCTION IF EXISTS gateway.usage_limit_set(text, uuid, bigint);
REVOKE ALL ON gateway.usage_limits FROM upmo_app;
DROP POLICY IF EXISTS usage_limits_select ON gateway.usage_limits;
DROP POLICY IF EXISTS usage_limits_service ON gateway.usage_limits;
ALTER TABLE gateway.usage_limits NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.usage_limits DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ledger_select ON gateway.ledger;
DROP POLICY IF EXISTS ledger_service ON gateway.ledger;
ALTER TABLE gateway.ledger NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.ledger DISABLE ROW LEVEL SECURITY;

REVOKE INSERT, UPDATE, DELETE ON gateway.route_links FROM upmo_app;
REVOKE INSERT, UPDATE, DELETE ON gateway.routes FROM upmo_app;

DROP POLICY IF EXISTS route_links_delete ON gateway.route_links;
DROP POLICY IF EXISTS route_links_update ON gateway.route_links;
DROP POLICY IF EXISTS route_links_insert ON gateway.route_links;
DROP POLICY IF EXISTS route_links_select ON gateway.route_links;
DROP POLICY IF EXISTS route_links_service ON gateway.route_links;
DROP POLICY IF EXISTS routes_delete ON gateway.routes;
DROP POLICY IF EXISTS routes_update ON gateway.routes;
DROP POLICY IF EXISTS routes_insert ON gateway.routes;
DROP POLICY IF EXISTS routes_select ON gateway.routes;
DROP POLICY IF EXISTS routes_service ON gateway.routes;

ALTER TABLE gateway.route_links NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.route_links DISABLE ROW LEVEL SECURITY;
ALTER TABLE gateway.routes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.routes DISABLE ROW LEVEL SECURITY;

DROP FUNCTION IF EXISTS gateway.route_has_private_account(uuid);
DROP FUNCTION IF EXISTS gateway.provider_account_used_by_shared_route(uuid);
DROP FUNCTION IF EXISTS gateway.shared_route_name_taken(text, uuid);

DELETE FROM gateway.route_links
WHERE route_id IN (SELECT id FROM gateway.routes WHERE NOT installed);
DELETE FROM gateway.routes WHERE NOT installed;

DROP INDEX IF EXISTS gateway.routes_team_ids;
DROP INDEX IF EXISTS gateway.routes_shared_name_uidx;
DROP INDEX IF EXISTS gateway.routes_private_name_uidx;
CREATE UNIQUE INDEX routes_name ON gateway.routes (name);

ALTER TABLE gateway.routes
  DROP CONSTRAINT IF EXISTS routes_installed_visibility_check,
  DROP CONSTRAINT IF EXISTS routes_installed_owner_check,
  DROP CONSTRAINT IF EXISTS routes_team_ids_check,
  DROP CONSTRAINT IF EXISTS routes_visibility_check;

ALTER TABLE gateway.routes
  DROP COLUMN IF EXISTS updated_at,
  DROP COLUMN IF EXISTS installed,
  DROP COLUMN IF EXISTS team_ids,
  DROP COLUMN IF EXISTS visibility,
  DROP COLUMN IF EXISTS owner_user_id;
