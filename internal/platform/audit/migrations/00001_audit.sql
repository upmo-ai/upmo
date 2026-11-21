-- +goose Up

-- The request pools run as upmo_app, which migrates after this schema on a
-- fresh install. Same guarded creation as identity, so parallel migrators
-- that race on the role name stay safe.
-- +goose StatementBegin
DO $$
BEGIN
  CREATE ROLE upmo_app NOLOGIN;
EXCEPTION
  WHEN duplicate_object OR unique_violation THEN NULL;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA audit TO upmo_app;

CREATE TABLE audit.pending (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  service text NOT NULL,
  action text NOT NULL,
  outcome text NOT NULL,
  reason text NOT NULL DEFAULT '',
  actor_type text NOT NULL,
  actor_id uuid,
  actor_role_id uuid,
  credential_type text NOT NULL,
  credential_id uuid,
  target_type text NOT NULL DEFAULT '',
  target_id text NOT NULL DEFAULT '',
  changes jsonb NOT NULL DEFAULT '{}',
  request_id uuid,
  source_ip inet,
  user_agent text NOT NULL DEFAULT '',
  CONSTRAINT pending_service_check CHECK (service IN ('brain', 'gateway', 'sandboxd')),
  CONSTRAINT pending_action_check CHECK (action ~ '^[a-z_]+(\.[a-z_]+)+$' AND char_length(action) <= 80),
  CONSTRAINT pending_outcome_check CHECK (outcome IN ('success', 'denied', 'failure')),
  CONSTRAINT pending_reason_check CHECK (char_length(reason) <= 100),
  CONSTRAINT pending_actor_check CHECK (
    (actor_type = 'user' AND actor_id IS NOT NULL)
    OR (actor_type IN ('system', 'anonymous') AND actor_id IS NULL)
  ),
  CONSTRAINT pending_credential_check CHECK (credential_type IN ('session', 'token', 'run', 'service', 'none')),
  CONSTRAINT pending_target_check CHECK (char_length(target_type) <= 40 AND char_length(target_id) <= 200),
  CONSTRAINT pending_changes_check CHECK (pg_column_size(changes) <= 8192),
  CONSTRAINT pending_agent_check CHECK (char_length(user_agent) <= 300)
);

CREATE TABLE audit.events (
  position bigint NOT NULL,
  sealed_at timestamptz NOT NULL,
  prev_hash bytea NOT NULL,
  hash bytea NOT NULL,
  id uuid NOT NULL,
  occurred_at timestamptz NOT NULL,
  service text NOT NULL,
  action text NOT NULL,
  outcome text NOT NULL,
  reason text NOT NULL,
  actor_type text NOT NULL,
  actor_id uuid,
  actor_role_id uuid,
  credential_type text NOT NULL,
  credential_id uuid,
  target_type text NOT NULL,
  target_id text NOT NULL,
  changes jsonb NOT NULL,
  request_id uuid,
  source_ip inet,
  user_agent text NOT NULL,
  PRIMARY KEY (position, sealed_at),
  CONSTRAINT events_service_check CHECK (service IN ('brain', 'gateway', 'sandboxd')),
  CONSTRAINT events_action_check CHECK (action ~ '^[a-z_]+(\.[a-z_]+)+$' AND char_length(action) <= 80),
  CONSTRAINT events_outcome_check CHECK (outcome IN ('success', 'denied', 'failure')),
  CONSTRAINT events_reason_check CHECK (char_length(reason) <= 100),
  CONSTRAINT events_actor_check CHECK (
    (actor_type = 'user' AND actor_id IS NOT NULL)
    OR (actor_type IN ('system', 'anonymous') AND actor_id IS NULL)
  ),
  CONSTRAINT events_credential_check CHECK (credential_type IN ('session', 'token', 'run', 'service', 'none')),
  CONSTRAINT events_target_check CHECK (char_length(target_type) <= 40 AND char_length(target_id) <= 200),
  CONSTRAINT events_changes_check CHECK (pg_column_size(changes) <= 8192),
  CONSTRAINT events_agent_check CHECK (char_length(user_agent) <= 300)
) PARTITION BY RANGE (sealed_at);

CREATE TABLE audit.events_y2026m11 PARTITION OF audit.events FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
CREATE TABLE audit.events_y2026m12 PARTITION OF audit.events FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');
CREATE TABLE audit.events_y2027m01 PARTITION OF audit.events FOR VALUES FROM ('2027-01-01') TO ('2027-02-01');
CREATE TABLE audit.events_default PARTITION OF audit.events DEFAULT;

CREATE INDEX events_time ON audit.events (occurred_at DESC);
CREATE INDEX events_actor ON audit.events (actor_id, occurred_at DESC) WHERE actor_id IS NOT NULL;
CREATE INDEX events_target ON audit.events (target_type, target_id, occurred_at DESC) WHERE target_id <> '';
CREATE INDEX events_action ON audit.events (action, occurred_at DESC);

-- One row. The sealer locks it FOR UPDATE, so only one sealer seals at a time.
CREATE TABLE audit.head (
  one boolean PRIMARY KEY DEFAULT true CHECK (one),
  position bigint NOT NULL,
  hash bytea NOT NULL,
  sealed_at timestamptz NOT NULL
);
INSERT INTO audit.head (position, hash, sealed_at) VALUES (0, decode(repeat('00', 32), 'hex'), '-infinity');

-- True for the role that owns the audit tables: the migrator and the sealer.
-- Requests run as upmo_app and never reach this branch. Created after head:
-- creating a SQL function checks its body, so the table must exist first.
-- +goose StatementBegin
CREATE FUNCTION audit.owner()
RETURNS boolean
LANGUAGE sql
STABLE
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT current_user = (
    SELECT pg_get_userbyid(c.relowner)
    FROM pg_class AS c
    WHERE c.oid = 'audit.head'::regclass
  );
$$;
-- +goose StatementEnd

-- Written before a partition is dropped: where the remaining chain starts.
CREATE TABLE audit.anchors (
  position bigint PRIMARY KEY,
  hash bytea NOT NULL,
  purged_before timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE FUNCTION audit.events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit events cannot be changed'; END $$;
-- +goose StatementEnd
CREATE TRIGGER events_no_change BEFORE UPDATE OR DELETE ON audit.events FOR EACH ROW EXECUTE FUNCTION audit.events_immutable();
CREATE TRIGGER events_no_truncate BEFORE TRUNCATE ON audit.events FOR EACH STATEMENT EXECUTE FUNCTION audit.events_immutable();

ALTER TABLE audit.pending ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.pending FORCE ROW LEVEL SECURITY;
CREATE POLICY pending_owner ON audit.pending FOR ALL USING (audit.owner()) WITH CHECK (audit.owner());
CREATE POLICY pending_app_insert ON audit.pending FOR INSERT TO upmo_app WITH CHECK (true);

ALTER TABLE audit.events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.events FORCE ROW LEVEL SECURITY;
CREATE POLICY events_owner ON audit.events FOR ALL USING (audit.owner()) WITH CHECK (audit.owner());
CREATE POLICY events_app_read ON audit.events FOR SELECT TO upmo_app
  USING ('audit:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')));

GRANT INSERT ON audit.pending TO upmo_app;
GRANT SELECT ON audit.events TO upmo_app;
REVOKE ALL ON audit.goose_db_version FROM upmo_app;

-- Where the audit push stopped: the highest position each target accepted.
-- A missing row means position 0. Only the owner role touches this table; the
-- request role is granted nothing on it.
CREATE TABLE audit.push_cursor (
  target text PRIMARY KEY CHECK (target IN ('webhook', 'syslog')),
  position bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE IF EXISTS audit.push_cursor;
DROP TABLE IF EXISTS audit.pending;
DROP TABLE IF EXISTS audit.events;
DROP TABLE IF EXISTS audit.head;
DROP TABLE IF EXISTS audit.anchors;
DROP FUNCTION IF EXISTS audit.events_immutable();
DROP FUNCTION IF EXISTS audit.owner();
REVOKE USAGE ON SCHEMA audit FROM upmo_app;
