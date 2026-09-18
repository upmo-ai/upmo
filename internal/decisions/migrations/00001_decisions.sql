-- +goose Up

CREATE TABLE decisions.gates (
  route text PRIMARY KEY,
  kind text NOT NULL,
  fingerprint text NOT NULL,
  verdict text NOT NULL,
  hit int NOT NULL,
  total int NOT NULL,
  bar int NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT gates_route_check CHECK (route <> ''),
  CONSTRAINT gates_kind_check CHECK (kind IN ('yesno', 'choice', 'filter')),
  CONSTRAINT gates_fingerprint_check CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
  CONSTRAINT gates_verdict_check CHECK (verdict = 'pass'),
  CONSTRAINT gates_score_check CHECK (hit >= 0 AND total > 0 AND hit <= total),
  CONSTRAINT gates_bar_check CHECK (bar BETWEEN 1 AND 100)
);

ALTER TABLE decisions.gates ENABLE ROW LEVEL SECURITY;
ALTER TABLE decisions.gates FORCE ROW LEVEL SECURITY;

CREATE POLICY gates_manage ON decisions.gates
  FOR ALL
  USING ('settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')))
  WITH CHECK ('settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ',')));

GRANT USAGE ON SCHEMA decisions TO upmo_app;
GRANT SELECT, INSERT, UPDATE ON decisions.gates TO upmo_app;

-- +goose Down

DROP TABLE IF EXISTS decisions.gates;
