-- +goose Up

CREATE TABLE chat.chats (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL DEFAULT 'chat',
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL,
  visibility_team_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT chats_type_check CHECK (object_type = 'chat'),
  CONSTRAINT chats_visibility_check CHECK (
    (visibility IN ('private', 'everyone') AND visibility_team_id IS NULL)
    OR (visibility = 'team' AND visibility_team_id IS NOT NULL)
  )
);

CREATE TABLE chat.lines (
  chat_id uuid NOT NULL REFERENCES chat.chats (id),
  seq int NOT NULL,
  body text NOT NULL,
  id uuid NOT NULL DEFAULT uuidv7(),
  role text NOT NULL DEFAULT 'user',
  at timestamptz NOT NULL DEFAULT now(),
  model text,
  tokens int NOT NULL DEFAULT 0,
  cost_micro_usd bigint,
  status text NOT NULL DEFAULT 'done',
  reply_to uuid,
  attempts jsonb,
  feed_id uuid NOT NULL DEFAULT uuidv7(),
  agent_id uuid,
  route text,
  pin_account_id uuid,
  pin_model text,
  author_id uuid,
  PRIMARY KEY (chat_id, seq),
  CONSTRAINT lines_seq_check CHECK (seq > 0),
  CONSTRAINT lines_id_key UNIQUE (id),
  CONSTRAINT lines_feed_id_key UNIQUE (feed_id),
  CONSTRAINT lines_reply_to_fkey FOREIGN KEY (reply_to) REFERENCES chat.lines (id),
  CONSTRAINT lines_role_check CHECK (role IN ('user', 'assistant')),
  CONSTRAINT lines_status_check CHECK (status IN ('done', 'failed')),
  CONSTRAINT lines_tokens_check CHECK (tokens >= 0),
  CONSTRAINT lines_cost_check CHECK (cost_micro_usd IS NULL OR cost_micro_usd >= 0),
  CONSTRAINT lines_body_check CHECK (
    char_length(body) <= CASE role WHEN 'user' THEN 4000 ELSE 32000 END
    AND (char_length(body) >= 1 OR (role = 'assistant' AND status = 'failed'))
  ),
  CONSTRAINT lines_user_bare_check CHECK (
    role <> 'user'
    OR (model IS NULL AND tokens = 0 AND cost_micro_usd IS NULL
        AND status = 'done' AND reply_to IS NULL AND attempts IS NULL)
  ),
  CONSTRAINT lines_assistant_model_check CHECK (
    role <> 'assistant'
    OR model IS NOT NULL
  ),
  CONSTRAINT lines_attempts_check CHECK (status = 'failed' OR attempts IS NULL),
  CONSTRAINT lines_route_check CHECK (route IS NULL OR char_length(route) BETWEEN 1 AND 64),
  CONSTRAINT lines_pin_check CHECK (
    (pin_account_id IS NULL AND pin_model IS NULL)
    OR (pin_account_id IS NOT NULL AND pin_model IS NOT NULL AND char_length(pin_model) BETWEEN 1 AND 120)
  ),
  CONSTRAINT lines_picks_user_check CHECK (
    role = 'user'
    OR (agent_id IS NULL AND route IS NULL AND pin_account_id IS NULL AND pin_model IS NULL AND author_id IS NULL)
  )
);

CREATE INDEX lines_reply_to_idx ON chat.lines (reply_to) WHERE reply_to IS NOT NULL;

ALTER TABLE chat.lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.lines FORCE ROW LEVEL SECURITY;

CREATE POLICY lines_select ON chat.lines
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY lines_insert ON chat.lines
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY lines_owner_select ON chat.lines
  FOR SELECT
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.lines'::regclass))
  );

-- The only line the owner stores is a failed reply.
CREATE POLICY lines_owner_fail ON chat.lines
  FOR INSERT
  WITH CHECK (
    role = 'assistant' AND status = 'failed'
    AND current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.lines'::regclass))
  );

-- +goose StatementBegin
CREATE FUNCTION chat.keep_line()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  RAISE EXCEPTION 'chat: writer';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER chat_keep_line
  BEFORE UPDATE OR DELETE ON chat.lines
  FOR EACH ROW
  EXECUTE FUNCTION chat.keep_line();

GRANT SELECT, INSERT ON chat.lines TO upmo_app;

SELECT identity.protect('chat.chats');

-- chat.fail_reply runs for a person who can no longer write the chat, so no
-- policy that asks for their access lets it through. It runs as the owner of
-- the tables, and each policy below admits that role for the rows it reads or
-- writes and nothing else, like deltas_sweep_select. They hold for an owner
-- that is neither a superuser nor BYPASSRLS, since FORCE applies to an owner
-- too. Nothing outside the function runs as the owner.
CREATE POLICY chats_owner_lock_select ON chat.chats
  FOR SELECT
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.chats'::regclass))
  );

-- SELECT ... FOR UPDATE needs an UPDATE policy to lock the row. WITH CHECK
-- (false) lets it lock and refuses any real update.
CREATE POLICY chats_owner_lock_update ON chat.chats
  FOR UPDATE
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.chats'::regclass))
  )
  WITH CHECK (false);

-- Text a reply has written so far, and the notices of models that failed
-- before it. A reply writes its deltas while it streams and deletes them in
-- the transaction that stores its line. line_id is the id that line will get.
-- A text row with seq 0 and no text marks the reply as started.
CREATE TABLE chat.deltas (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid NOT NULL,
  seq int NOT NULL,
  kind text NOT NULL DEFAULT 'text',
  body text NOT NULL,
  at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT deltas_kind_check CHECK (kind IN ('text', 'status')),
  CONSTRAINT deltas_line_seq_key UNIQUE (line_id, seq)
);

CREATE INDEX deltas_chat_idx ON chat.deltas (chat_id, id);

ALTER TABLE chat.deltas ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.deltas FORCE ROW LEVEL SECURITY;

CREATE POLICY deltas_select ON chat.deltas
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY deltas_insert ON chat.deltas
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY deltas_delete ON chat.deltas
  FOR DELETE
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY deltas_sweep_select ON chat.deltas
  FOR SELECT
  USING (
    at < now() - interval '2 hours'
    AND current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.deltas'::regclass))
  );

CREATE POLICY deltas_sweep_delete ON chat.deltas
  FOR DELETE
  USING (
    at < now() - interval '2 hours'
    AND current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.deltas'::regclass))
  );

CREATE POLICY deltas_owner_select ON chat.deltas
  FOR SELECT
  USING (
    kind = 'text'
    AND current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.deltas'::regclass))
  );

-- Owner-wide on purpose: a policy that also read chat.lines would need the
-- owner to hold that privilege. chat.fail_reply and chat.sweep_deltas fix
-- what they delete, the sweep by age, so nothing else runs as the owner.
CREATE POLICY deltas_owner_delete ON chat.deltas
  FOR DELETE
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.deltas'::regclass))
  );

GRANT SELECT, INSERT, DELETE ON chat.deltas TO upmo_app;

-- A delta or a line announces itself to the stream hub of every brain
-- process. The payload carries ids only: the hub reads the rows. NOTIFY is
-- queued in the writing transaction and sent when it commits, so a rollback
-- sends nothing. It is not durable: the hub replays from the tables after a
-- reconnect and on a timer.

-- +goose StatementBegin
CREATE FUNCTION chat.stream_notify()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  PERFORM pg_notify('chat_stream', json_build_object('chat', NEW.chat_id, 'id', NEW.id)::text);
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER chat_deltas_stream_notify
  AFTER INSERT ON chat.deltas
  FOR EACH ROW
  EXECUTE FUNCTION chat.stream_notify();

CREATE TRIGGER chat_lines_stream_notify
  AFTER INSERT ON chat.lines
  FOR EACH ROW
  EXECUTE FUNCTION chat.stream_notify();

-- +goose StatementBegin
CREATE FUNCTION chat.sweep_deltas()
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  swept bigint;
BEGIN
  DELETE FROM chat.deltas AS delta
  WHERE delta.at < now() - interval '2 hours';
  GET DIAGNOSTICS swept = ROW_COUNT;
  RETURN swept;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION chat.sweep_deltas() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.sweep_deltas() TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION chat.fail_reply(user_line uuid, reply uuid, why text)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  in_chat uuid;
BEGIN
  IF why NOT IN ('access', 'unwritten') THEN
    RAISE EXCEPTION 'chat: fail_reply';
  END IF;
  SELECT line.chat_id INTO in_chat
  FROM chat.lines AS line
  WHERE line.id = user_line AND line.role = 'user';
  IF in_chat IS NULL OR NOT EXISTS (
       SELECT 1 FROM platform.river_job AS job
       WHERE job.kind = 'chat_reply'
         AND job.state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
         AND job.args ->> 'line_id' = user_line::text
         AND job.args ->> 'reply_id' = reply::text
         AND job.args ->> 'chat_id' = in_chat::text) THEN
    RETURN 'none';
  END IF;
  PERFORM 1 FROM chat.chats WHERE id = in_chat FOR UPDATE;
  IF EXISTS (SELECT 1 FROM chat.lines AS line WHERE line.id = reply)
     OR EXISTS (SELECT 1 FROM chat.lines AS line WHERE line.reply_to = user_line AND line.status = 'done') THEN
    RETURN 'answered';
  END IF;
  INSERT INTO chat.lines (id, chat_id, seq, role, body, model, status, reply_to, attempts)
  SELECT reply, in_chat, COALESCE(max(line.seq), 0) + 1, 'assistant',
         left(COALESCE((SELECT string_agg(delta.body, '' ORDER BY delta.seq)
                        FROM chat.deltas AS delta
                        WHERE delta.chat_id = in_chat AND delta.line_id = reply
                          AND delta.kind = 'text'), ''), 32000),
         '', 'failed', user_line,
         jsonb_build_array(jsonb_build_object('model', '', 'class', why))
  FROM chat.lines AS line WHERE line.chat_id = in_chat;
  DELETE FROM chat.deltas WHERE chat_id = in_chat AND line_id = reply;
  RETURN 'stored';
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION chat.fail_reply(uuid, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.fail_reply(uuid, uuid, text) TO upmo_app;

CREATE TABLE chat.turns (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid,
  seq int NOT NULL,
  role text NOT NULL,
  body text NOT NULL DEFAULT '',
  tool_calls jsonb,
  tool_name text NOT NULL DEFAULT '',
  tool_id text NOT NULL DEFAULT '',
  is_error boolean NOT NULL DEFAULT false,
  offloaded boolean NOT NULL DEFAULT false,
  tainted boolean NOT NULL DEFAULT false,
  private_read boolean NOT NULL DEFAULT false,
  model text NOT NULL DEFAULT '',
  input_tokens int NOT NULL DEFAULT 0,
  output_tokens int NOT NULL DEFAULT 0,
  cost_micro_usd bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (chat_id, seq),
  CONSTRAINT chat_turns_seq_check CHECK (seq > 0),
  CONSTRAINT chat_turns_role_check CHECK (role IN ('user', 'assistant', 'tool', 'blocked', 'simulated'))
);

CREATE INDEX chat_turns_chat_idx ON chat.turns (chat_id, seq);

ALTER TABLE chat.turns ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.turns FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_turns_owner ON chat.turns
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.turns'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.turns'::regclass))
  );

CREATE POLICY chat_turns_select ON chat.turns
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY chat_turns_insert ON chat.turns
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

GRANT SELECT, INSERT ON chat.turns TO upmo_app;

CREATE TABLE chat.holds (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid,
  user_id uuid NOT NULL,
  reason text NOT NULL,
  action text NOT NULL,
  preview text NOT NULL DEFAULT '',
  calls jsonb[] NOT NULL DEFAULT '{}'::jsonb[],
  untrusted_origin boolean NOT NULL DEFAULT false,
  state text NOT NULL DEFAULT 'pending',
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  CONSTRAINT holds_reason_check CHECK (reason IN ('ask', 'expired')),
  CONSTRAINT holds_action_check CHECK (char_length(action) BETWEEN 1 AND 200),
  CONSTRAINT holds_state_check CHECK (state IN ('pending', 'approved', 'denied', 'closed')),
  CONSTRAINT holds_decided_check CHECK ((state = 'pending') = (decided_at IS NULL))
);

CREATE UNIQUE INDEX holds_one_pending
  ON chat.holds (chat_id)
  WHERE state = 'pending';

CREATE INDEX holds_user_idx ON chat.holds (user_id, created_at DESC);

ALTER TABLE chat.holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.holds FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_holds_owner ON chat.holds
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.holds'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.holds'::regclass))
  );

CREATE POLICY chat_holds_select ON chat.holds
  FOR SELECT
  USING (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:view' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

CREATE POLICY chat_holds_insert ON chat.holds
  FOR INSERT
  WITH CHECK (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  );

CREATE POLICY chat_holds_update ON chat.holds
  FOR UPDATE
  USING (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  )
  WITH CHECK (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
  );

GRANT SELECT, INSERT ON chat.holds TO upmo_app;
GRANT UPDATE (state, decided_at) ON chat.holds TO upmo_app;

CREATE TABLE chat.action_logs (
  scope_id uuid NOT NULL,
  call_id text NOT NULL,
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid,
  user_id uuid NOT NULL,
  tool_name text NOT NULL,
  action text NOT NULL DEFAULT '',
  preview text NOT NULL DEFAULT '',
  state text NOT NULL,
  untrusted_origin boolean NOT NULL DEFAULT false,
  dry_run boolean NOT NULL DEFAULT false,
  response_digest text NOT NULL DEFAULT '',
  error_text text NOT NULL DEFAULT '',
  executed_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (scope_id, call_id),
  CONSTRAINT chat_action_logs_call_id_check CHECK (char_length(call_id) BETWEEN 1 AND 200),
  CONSTRAINT chat_action_logs_state_check CHECK (state IN ('dialing', 'done', 'failed', 'simulated'))
);

CREATE INDEX chat_action_logs_chat_idx ON chat.action_logs (chat_id, executed_at DESC);

ALTER TABLE chat.action_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.action_logs FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_action_logs_owner ON chat.action_logs
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.action_logs'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.action_logs'::regclass))
  );

CREATE POLICY chat_action_logs_select ON chat.action_logs
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY chat_action_logs_insert ON chat.action_logs
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY chat_action_logs_update ON chat.action_logs
  FOR UPDATE
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ))
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

GRANT SELECT, INSERT ON chat.action_logs TO upmo_app;
GRANT UPDATE (state, response_digest, error_text, completed_at) ON chat.action_logs TO upmo_app;

CREATE TABLE chat.findings (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid,
  kind text NOT NULL DEFAULT 'output',
  title text NOT NULL DEFAULT '',
  body text NOT NULL,
  source_ref text NOT NULL DEFAULT '',
  untrusted boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT chat_findings_kind_check CHECK (kind IN ('finding', 'output', 'tool_output'))
);

CREATE INDEX chat_findings_chat_idx ON chat.findings (chat_id, created_at);

ALTER TABLE chat.findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.findings FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_findings_owner ON chat.findings
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.findings'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.findings'::regclass))
  );

CREATE POLICY chat_findings_select ON chat.findings
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY chat_findings_insert ON chat.findings
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

GRANT SELECT, INSERT ON chat.findings TO upmo_app;

-- +goose Down

DROP TABLE IF EXISTS chat.findings;
DROP TABLE IF EXISTS chat.action_logs;
DROP TABLE IF EXISTS chat.holds;
DROP TABLE IF EXISTS chat.turns;
DROP FUNCTION IF EXISTS chat.fail_reply(uuid, uuid, text);
DROP FUNCTION IF EXISTS chat.sweep_deltas();
DROP TRIGGER IF EXISTS chat_lines_stream_notify ON chat.lines;
DROP TRIGGER IF EXISTS chat_deltas_stream_notify ON chat.deltas;
DROP FUNCTION IF EXISTS chat.stream_notify();
DROP TABLE IF EXISTS chat.deltas;
DROP TABLE IF EXISTS chat.lines;
DROP TABLE IF EXISTS chat.chats;
DROP FUNCTION IF EXISTS chat.keep_line();
