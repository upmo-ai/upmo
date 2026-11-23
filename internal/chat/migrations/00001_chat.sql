-- +goose Up

CREATE TABLE chat.chats (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL DEFAULT 'chat',
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL,
  visibility_team_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  title text NOT NULL DEFAULT '',
  tainted boolean NOT NULL DEFAULT false,
  private_read boolean NOT NULL DEFAULT false,
  rolling_summary text NOT NULL DEFAULT '',
  summary_up_to_line_id uuid,
  summary_up_to_seq int NOT NULL DEFAULT 0,
  CONSTRAINT chats_type_check CHECK (object_type = 'chat'),
  CONSTRAINT chats_visibility_check CHECK (
    (visibility IN ('private', 'everyone') AND visibility_team_id IS NULL)
    OR (visibility = 'team' AND visibility_team_id IS NOT NULL)
  ),
  CONSTRAINT chats_summary_seq_check CHECK (
    (summary_up_to_seq = 0 AND summary_up_to_line_id IS NULL AND rolling_summary = '')
    OR (summary_up_to_seq > 0 AND summary_up_to_line_id IS NOT NULL)
  ),
  CONSTRAINT chats_title_check CHECK (char_length(title) <= 60)
);

CREATE TABLE chat.lines (
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
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
  attachments jsonb,
  body_tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', left(body, 64000))) STORED,
  PRIMARY KEY (chat_id, seq),
  CONSTRAINT lines_seq_check CHECK (seq > 0),
  CONSTRAINT lines_id_key UNIQUE (id),
  CONSTRAINT lines_feed_id_key UNIQUE (feed_id),
  CONSTRAINT lines_reply_to_fkey FOREIGN KEY (reply_to) REFERENCES chat.lines (id) ON DELETE CASCADE,
  CONSTRAINT lines_role_check CHECK (role IN ('user', 'assistant')),
  CONSTRAINT lines_status_check CHECK (status IN ('done', 'failed', 'stopped')),
  CONSTRAINT lines_tokens_check CHECK (tokens >= 0),
  CONSTRAINT lines_cost_check CHECK (cost_micro_usd IS NULL OR cost_micro_usd >= 0),
  CONSTRAINT lines_body_check CHECK (
    char_length(body) <= CASE role WHEN 'user' THEN 32000 ELSE 64000 END
    AND (char_length(body) >= 1 OR (role = 'assistant' AND status IN ('failed', 'stopped')))
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
  ),
  CONSTRAINT lines_attachments_check CHECK (
    attachments IS NULL
    OR (
      role = 'user'
      AND jsonb_typeof(attachments) = 'array'
      AND jsonb_array_length(attachments) <= 10
    )
  )
);

CREATE INDEX lines_reply_to_idx ON chat.lines (reply_to) WHERE reply_to IS NOT NULL;
CREATE INDEX lines_body_fts_idx ON chat.lines USING gin (body_tsv);

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
  WITH CHECK (
    (
      role = 'assistant'
      OR author_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    )
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  );

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
  IF TG_OP = 'DELETE' AND current_user = pg_catalog.pg_get_userbyid((SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.lines'::regclass)) AND NULLIF(pg_catalog.current_setting('upmo.chat_delete', true), '')::uuid = OLD.chat_id THEN RETURN OLD; END IF;
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
REVOKE UPDATE (created_at, tainted, private_read, rolling_summary, summary_up_to_line_id, summary_up_to_seq) ON chat.chats FROM upmo_app;
DROP POLICY IF EXISTS may_delete ON chat.chats;
CREATE POLICY may_delete ON chat.chats FOR DELETE USING (
  owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
  AND 'chat:delete' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
);

-- +goose StatementBegin
CREATE FUNCTION chat.delete_chat(target_chat_id uuid)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE
  caller uuid := NULLIF(pg_catalog.current_setting('upmo.user_id', true), '')::uuid;
  allow_list text[] := pg_catalog.string_to_array(NULLIF(pg_catalog.current_setting('upmo.allow', true), ''), ',');
  c chat.chats%ROWTYPE;
BEGIN
  SELECT * INTO c FROM chat.chats WHERE id = target_chat_id FOR UPDATE;
  IF NOT FOUND OR NOT identity.may('view', c.object_type, c.owner_user_id, c.visibility, c.visibility_team_id, c.id) THEN
    RAISE EXCEPTION 'chat: no such chat' USING ERRCODE = 'P0002';
  END IF;
  IF caller IS NULL OR c.owner_user_id <> caller OR NOT ('chat:delete' = ANY (allow_list)) THEN
    RAISE EXCEPTION 'chat: access' USING ERRCODE = '42501';
  END IF;
  PERFORM pg_catalog.set_config('upmo.chat_delete', target_chat_id::text, true);
  DELETE FROM chat.chats WHERE id = target_chat_id;
  PERFORM pg_catalog.set_config('upmo.chat_delete', '', true);
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION chat.delete_chat(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.delete_chat(uuid) TO upmo_app;

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

-- SELECT ... FOR NO KEY UPDATE needs an UPDATE policy to lock the row.
-- chat.advance_chat_exposure also writes as the owner, so the check admits the
-- owner; chat.keep_chat then refuses anything but a monotonic exposure change.
CREATE POLICY chats_owner_lock_update ON chat.chats
  FOR UPDATE
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.chats'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.chats'::regclass))
  );

-- tainted and private_read only ever turn on, and only chat.advance_chat_exposure
-- (which runs as the table owner) turns them on. visibility is the owner's.
-- +goose StatementBegin
CREATE FUNCTION chat.keep_chat()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
  actor uuid := NULLIF(current_setting('upmo.user_id', true), '')::uuid;
  is_owner boolean := (actor IS NOT NULL AND actor = OLD.owner_user_id);
  is_table_owner boolean := (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.chats'::regclass))
  );
BEGIN
  IF (OLD.tainted AND NOT NEW.tainted) OR (OLD.private_read AND NOT NEW.private_read) THEN
    RAISE EXCEPTION 'chat: taint';
  END IF;
  IF (NEW.tainted IS DISTINCT FROM OLD.tainted OR NEW.private_read IS DISTINCT FROM OLD.private_read)
     AND NOT is_table_owner THEN
    RAISE EXCEPTION 'chat: taint';
  END IF;
  IF (NEW.rolling_summary IS DISTINCT FROM OLD.rolling_summary
      OR NEW.summary_up_to_line_id IS DISTINCT FROM OLD.summary_up_to_line_id
      OR NEW.summary_up_to_seq IS DISTINCT FROM OLD.summary_up_to_seq) THEN
    IF NEW.summary_up_to_seq < OLD.summary_up_to_seq OR NOT is_table_owner THEN
      RAISE EXCEPTION 'chat: summary';
    END IF;
  END IF;
  IF (NEW.visibility IS DISTINCT FROM OLD.visibility
      OR NEW.visibility_team_id IS DISTINCT FROM OLD.visibility_team_id)
     AND NOT (is_owner OR is_table_owner) THEN
    RAISE EXCEPTION 'chat: owner';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION chat.advance_chat_summary(
  target_chat_id uuid,
  active_run_id uuid,
  job_claim uuid,
  new_summary text,
  up_to_line uuid,
  up_to_seq int
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  c chat.chats%ROWTYPE;
BEGIN
  SELECT * INTO c FROM chat.chats WHERE id = target_chat_id FOR NO KEY UPDATE;
  IF NOT FOUND OR NOT identity.may('edit', c.object_type, c.owner_user_id, c.visibility, c.visibility_team_id, c.id) THEN
    RAISE EXCEPTION 'chat: access' USING ERRCODE = '42501';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM chat.runs AS r
    WHERE r.id = active_run_id AND r.chat_id = target_chat_id
      AND r.state = 'running' AND r.claim = job_claim
  ) THEN
    RAISE EXCEPTION 'chat: fenced' USING ERRCODE = '42501';
  END IF;
  IF up_to_seq <= c.summary_up_to_seq OR COALESCE(new_summary, '') = '' THEN
    RETURN;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM chat.lines AS l
    WHERE l.chat_id = target_chat_id AND l.id = up_to_line AND l.seq = up_to_seq
  ) THEN
    RAISE EXCEPTION 'chat: summary' USING ERRCODE = '22023';
  END IF;
  UPDATE chat.chats
  SET rolling_summary = new_summary, summary_up_to_line_id = up_to_line,
      summary_up_to_seq = up_to_seq
  WHERE id = target_chat_id;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION chat.advance_chat_summary(uuid, uuid, uuid, text, uuid, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.advance_chat_summary(uuid, uuid, uuid, text, uuid, int) TO upmo_app;

CREATE TRIGGER chat_keep_chat
  BEFORE UPDATE ON chat.chats
  FOR EACH ROW
  EXECUTE FUNCTION chat.keep_chat();

-- +goose StatementBegin
CREATE FUNCTION chat.advance_chat_exposure(
  target_chat_id uuid,
  add_tainted boolean,
  add_private_read boolean
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  c chat.chats%ROWTYPE;
BEGIN
  IF NOT COALESCE(add_tainted, false) AND NOT COALESCE(add_private_read, false) THEN
    RETURN;
  END IF;
  SELECT * INTO c FROM chat.chats WHERE id = target_chat_id FOR NO KEY UPDATE;
  IF NOT FOUND OR NOT identity.may('edit', c.object_type, c.owner_user_id, c.visibility, c.visibility_team_id, c.id) THEN
    RAISE EXCEPTION 'chat: access' USING ERRCODE = '42501';
  END IF;
  UPDATE chat.chats
  SET tainted = tainted OR COALESCE(add_tainted, false),
      private_read = private_read OR COALESCE(add_private_read, false)
  WHERE id = target_chat_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION chat.advance_chat_exposure(uuid, boolean, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.advance_chat_exposure(uuid, boolean, boolean) TO upmo_app;

-- Text a reply has written so far, and the notices of models that failed
-- before it. A reply writes its deltas while it streams and deletes them in
-- the transaction that stores its line. line_id is the run id (chat.runs.id).
-- A text row with seq 0 and no text, written once by the round-1 claim, marks
-- the reply as started. seq goes on across the rounds of one reply.
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

-- A reply in progress and its fence. id is the id the assistant line will
-- get and the run id the gateway ledger books. One running row per chat at
-- most. claim is the token of the worker that owns the current round, and
-- claim_round that round; every write of a reply checks them under this
-- row's lock. history is the model history after the last committed round.
-- round_seq is the highest delta seq when the current round was claimed: the
-- status notices after it are that round's own, and only they say which model
-- the round was writing with.
CREATE TABLE chat.runs (
  id uuid PRIMARY KEY,
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  line_id uuid NOT NULL REFERENCES chat.lines (id) ON DELETE CASCADE,
  author_id uuid NOT NULL,
  state text NOT NULL DEFAULT 'running',
  round int NOT NULL DEFAULT 0,
  claim uuid,
  claim_round int NOT NULL DEFAULT 0,
  round_seq int NOT NULL DEFAULT 0,
  history jsonb,
  tokens int NOT NULL DEFAULT 0,
  cost_micro_usd bigint NOT NULL DEFAULT 0,
  cost_known boolean NOT NULL DEFAULT true,
  model text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  CONSTRAINT runs_state_check CHECK (state IN ('running', 'waiting', 'done', 'failed', 'stopped')),
  CONSTRAINT runs_round_check CHECK (round BETWEEN 0 AND 15 AND claim_round BETWEEN round AND round + 1),
  CONSTRAINT runs_tokens_check CHECK (tokens >= 0),
  CONSTRAINT runs_cost_check CHECK (cost_micro_usd >= 0),
  CONSTRAINT runs_finished_check CHECK ((state IN ('running', 'waiting')) = (finished_at IS NULL)),
  CONSTRAINT runs_history_check CHECK (state IN ('running', 'waiting') OR (history IS NULL AND claim IS NULL)),
  CONSTRAINT runs_history_type_check CHECK (history IS NULL OR jsonb_typeof(history) = 'array')
);

CREATE UNIQUE INDEX runs_one_running ON chat.runs (chat_id) WHERE state IN ('running', 'waiting');
CREATE INDEX runs_chat_idx ON chat.runs (chat_id);
CREATE INDEX runs_line_idx ON chat.runs (line_id);

ALTER TABLE chat.runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.runs FORCE ROW LEVEL SECURITY;

-- history holds tool output, so a viewer reads none of it: edit for all.
CREATE POLICY runs_select ON chat.runs
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

CREATE POLICY runs_insert ON chat.runs
  FOR INSERT
  WITH CHECK (
    author_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  );

-- Also what SELECT ... FOR UPDATE / FOR SHARE on a run needs.
CREATE POLICY runs_update ON chat.runs
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

-- +goose StatementBegin
CREATE FUNCTION chat.close_waiting_run(target_chat_id uuid, new_state text, hold_reason text DEFAULT NULL)
RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE
  c record;
  r record;
  v_call_stack text;
  is_table_owner boolean;
BEGIN
  is_table_owner := session_user = pg_catalog.pg_get_userbyid((SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.chats'::regclass));
  IF new_state NOT IN ('stopped', 'failed') THEN RAISE EXCEPTION 'chat: state' USING ERRCODE = '22023'; END IF;
  SELECT * INTO c FROM chat.chats WHERE id = target_chat_id FOR NO KEY UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'chat: no such chat' USING ERRCODE = 'P0002'; END IF;
  IF new_state = 'stopped' THEN
    IF NOT is_table_owner AND NOT identity.may('edit', c.object_type, c.owner_user_id, c.visibility, c.visibility_team_id, c.id) THEN RAISE EXCEPTION 'chat: access' USING ERRCODE = '42501'; END IF;
  ELSE
    GET DIAGNOSTICS v_call_stack = PG_CONTEXT;
    IF NOT is_table_owner AND pg_catalog.strpos(v_call_stack, 'fail_waiting_runs(') = 0 THEN RAISE EXCEPTION 'chat: access' USING ERRCODE = '42501'; END IF;
  END IF;
  SELECT * INTO r FROM chat.runs WHERE chat_id = target_chat_id AND state IN ('running', 'waiting') FOR UPDATE;
  IF NOT FOUND THEN RETURN NULL; END IF;
  UPDATE chat.holds SET reason = COALESCE(NULLIF(hold_reason, ''), reason), state = 'closed', decided_at = now() WHERE chat_id = target_chat_id AND state = 'pending';
  UPDATE chat.runs SET state = new_state, claim = NULL, history = NULL, finished_at = now() WHERE id = r.id;
  RETURN r.id;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION chat.close_waiting_run(uuid, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.close_waiting_run(uuid, text, text) TO upmo_app;

-- For chat.fail_reply only, owner-wide like deltas_owner_delete.
CREATE POLICY runs_owner_select ON chat.runs
  FOR SELECT
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.runs'::regclass))
  );

CREATE POLICY runs_owner_update ON chat.runs
  FOR UPDATE
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.runs'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.runs'::regclass))
  );

GRANT SELECT, INSERT ON chat.runs TO upmo_app;
GRANT UPDATE (state, round, claim, claim_round, round_seq, history, tokens, cost_micro_usd, cost_known, model, created_at, finished_at)
  ON chat.runs TO upmo_app;

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
CREATE FUNCTION chat.fail_waiting_runs(target_chat_id uuid DEFAULT NULL)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE rec record; reply_body text; fail_class text; failed_count bigint := 0;
BEGIN
  FOR rec IN
    SELECT run.id AS run_id, run.chat_id, run.line_id, run.author_id, run.model, run.tokens,
           run.created_at AS run_created_at, hold.created_at AS hold_created_at,
           chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id
    FROM chat.runs run JOIN chat.chats chat ON chat.id = run.chat_id
    LEFT JOIN chat.holds hold ON hold.chat_id = run.chat_id AND hold.state = 'pending'
    WHERE run.state = 'waiting' AND (target_chat_id IS NULL OR run.chat_id = target_chat_id)
    FOR UPDATE OF run
  LOOP
    fail_class := NULL;
    IF COALESCE(rec.hold_created_at, rec.run_created_at, now()) <= now() - interval '24 hours' THEN
      fail_class := 'timeout';
    ELSIF target_chat_id IS NOT NULL AND NOT EXISTS (
      SELECT 1 FROM identity.users u JOIN identity.roles role ON role.id = u.role_id
      WHERE u.id = rec.author_id AND u.disabled_at IS NULL AND 'chat:edit' = ANY (identity.role_permissions(role.key, role.permissions))
        AND (rec.owner_user_id = rec.author_id
          OR EXISTS (SELECT 1 FROM identity.shares share WHERE share.object_type = 'chat' AND share.object_id = rec.chat_id AND share.level = 'editor'
            AND (share.user_id = rec.author_id OR share.grantee_role_id = u.role_id OR EXISTS (SELECT 1 FROM identity.team_members tm WHERE tm.team_id = share.team_id AND tm.user_id = rec.author_id)))
        )
    ) THEN fail_class := 'access';
    END IF;
    IF fail_class IS NOT NULL THEN
      SELECT COALESCE(string_agg(delta.body, '' ORDER BY delta.seq), '') INTO reply_body
      FROM chat.deltas delta WHERE delta.chat_id = rec.chat_id AND delta.line_id = rec.run_id AND delta.kind = 'text';
      INSERT INTO chat.lines (id, chat_id, seq, role, body, model, tokens, status, reply_to, attempts)
      SELECT rec.run_id, rec.chat_id, COALESCE(max(line.seq), 0) + 1, 'assistant', left(reply_body, 64000),
        CASE WHEN rec.model <> '' THEN rec.model ELSE 'failed' END, rec.tokens, 'failed', rec.line_id,
        jsonb_build_array(jsonb_build_object('model', CASE WHEN rec.model <> '' THEN rec.model ELSE 'failed' END, 'class', fail_class))
      FROM chat.lines line WHERE line.chat_id = rec.chat_id;
      DELETE FROM chat.deltas WHERE chat_id = rec.chat_id AND line_id = rec.run_id;
      PERFORM chat.close_waiting_run(rec.chat_id, 'failed', CASE WHEN fail_class = 'timeout' THEN 'expired' ELSE NULL END);
      failed_count := failed_count + 1;
    END IF;
  END LOOP;
  RETURN failed_count;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION chat.fail_waiting_runs(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.fail_waiting_runs(uuid) TO upmo_app;

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
  PERFORM chat.fail_waiting_runs(NULL);
  DELETE FROM chat.deltas AS delta
  WHERE delta.at < now() - interval '2 hours'
    AND NOT EXISTS (SELECT 1 FROM chat.runs AS run WHERE run.id = delta.line_id AND run.state = 'waiting');
  GET DIAGNOSTICS swept = ROW_COUNT;
  RETURN swept;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION chat.sweep_deltas() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.sweep_deltas() TO upmo_app;

-- chat.fail_reply stores the failed reply of a run for an author who can no
-- longer write the chat, or for a job that could not start. It fails only the
-- run named and only for the job that owns its current round: the round is
-- next, and either nobody claimed it or the caller holds the claim. An older
-- worker is refused. It runs as the table owner; see chats_owner_lock_select.
-- +goose StatementBegin
CREATE FUNCTION chat.fail_reply(reply uuid, job_round int, job_claim uuid, why text)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  in_chat uuid;
  asked uuid;
  spent int;
BEGIN
  IF why NOT IN ('access', 'unwritten') THEN
    RAISE EXCEPTION 'chat: fail_reply';
  END IF;
  SELECT run.chat_id INTO in_chat FROM chat.runs AS run WHERE run.id = reply;
  IF in_chat IS NULL THEN
    RETURN 'none';
  END IF;
  PERFORM 1 FROM chat.chats WHERE id = in_chat FOR NO KEY UPDATE;
  SELECT run.line_id, run.tokens INTO asked, spent
  FROM chat.runs AS run
  WHERE run.id = reply AND run.state = 'running'
    AND run.round = job_round - 1
    AND (run.claim_round < job_round OR run.claim = job_claim)
  FOR UPDATE;
  IF asked IS NULL THEN
    RETURN 'fenced';
  END IF;
  INSERT INTO chat.lines (id, chat_id, seq, role, body, model, tokens, status, reply_to, attempts)
  SELECT reply, in_chat, COALESCE(max(line.seq), 0) + 1, 'assistant',
         left(COALESCE((SELECT string_agg(delta.body, '' ORDER BY delta.seq)
                        FROM chat.deltas AS delta
                        WHERE delta.chat_id = in_chat AND delta.line_id = reply
                          AND delta.kind = 'text'), ''), 64000),
         '', spent, 'failed', asked,
         jsonb_build_array(jsonb_build_object('model', '', 'class', why))
  FROM chat.lines AS line WHERE line.chat_id = in_chat;
  DELETE FROM chat.deltas WHERE chat_id = in_chat AND line_id = reply;
  UPDATE chat.runs
  SET state = 'failed', claim = NULL, history = NULL, finished_at = now()
  WHERE id = reply;
  RETURN 'stored';
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION chat.fail_reply(uuid, int, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.fail_reply(uuid, int, uuid, text) TO upmo_app;

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
  author_id uuid,
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

-- +goose StatementBegin
CREATE FUNCTION chat.redacted_turn_metadata(target_chat_id uuid)
RETURNS TABLE (line_id uuid, seq int, role text, tool_name text, tool_id text, is_error boolean, offloaded boolean, tainted boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
  SELECT turn.line_id, turn.seq, turn.role, turn.tool_name, turn.tool_id, turn.is_error, turn.offloaded, turn.tainted
  FROM chat.turns AS turn
  JOIN chat.chats AS chat ON chat.id = turn.chat_id
  WHERE turn.chat_id = target_chat_id
    AND turn.private_read
    AND (
      turn.author_id IS DISTINCT FROM NULLIF(pg_catalog.current_setting('upmo.user_id', true), '')::uuid
      OR chat.visibility <> 'private'
      OR EXISTS (SELECT 1 FROM identity.shares AS share WHERE share.object_type = 'chat' AND share.object_id = chat.id)
    )
    AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION chat.redacted_turn_metadata(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.redacted_turn_metadata(uuid) TO upmo_app;

CREATE POLICY chat_turns_select ON chat.turns
  FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
      AND (NOT chat.turns.private_read OR (
        chat.turns.author_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
        AND chat.visibility = 'private'
        AND NOT EXISTS (SELECT 1 FROM identity.shares AS share WHERE share.object_type = 'chat' AND share.object_id = chat.id)
      ))
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
  hold_turn_seq int NOT NULL DEFAULT 0,
  reason text NOT NULL,
  action text NOT NULL,
  preview text NOT NULL DEFAULT '',
  calls jsonb[] NOT NULL DEFAULT '{}'::jsonb[],
  untrusted_origin boolean NOT NULL DEFAULT false,
  state text NOT NULL DEFAULT 'pending',
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  CONSTRAINT holds_reason_check CHECK (reason IN ('ask', 'expired')),
  CONSTRAINT holds_turn_seq_check CHECK (hold_turn_seq >= 0),
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
    AND EXISTS (SELECT 1 FROM chat.chats chat WHERE chat.id = chat_id AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id))
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
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  )
  WITH CHECK (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND 'chat:edit' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  );

GRANT SELECT, INSERT ON chat.holds TO upmo_app;
GRANT UPDATE (state, decided_at) ON chat.holds TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION chat.decide_hold_once() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
  IF OLD.state <> 'pending' THEN RAISE EXCEPTION 'chat: hold'; END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER chat_holds_decide_once BEFORE UPDATE ON chat.holds FOR EACH ROW EXECUTE FUNCTION chat.decide_hold_once();

-- +goose StatementBegin
CREATE FUNCTION chat.hold_status(target_chat_id uuid, target_hold_id uuid,
  OUT chat_visible boolean, OUT hold_found boolean, OUT hold_user_id uuid, OUT hold_state text)
RETURNS RECORD LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE c chat.chats%ROWTYPE; h chat.holds%ROWTYPE;
BEGIN
  chat_visible := false; hold_found := false; hold_user_id := NULL; hold_state := NULL;
  SELECT * INTO c FROM chat.chats WHERE id = target_chat_id;
  IF NOT FOUND OR NOT identity.may('view', c.object_type, c.owner_user_id, c.visibility, c.visibility_team_id, c.id) THEN RETURN; END IF;
  chat_visible := true;
  SELECT * INTO h FROM chat.holds WHERE id = target_hold_id AND chat_id = target_chat_id;
  IF NOT FOUND THEN RETURN; END IF;
  hold_found := true; hold_user_id := h.user_id; hold_state := h.state;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION chat.hold_status(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat.hold_status(uuid, uuid) TO upmo_app;

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
  private_read boolean NOT NULL DEFAULT false,
  author_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT chat_findings_kind_check CHECK (kind IN ('finding', 'output', 'tool_output', 'attachment')),
  CONSTRAINT chat_findings_body_check CHECK (octet_length(body) <= 262144)
);

CREATE INDEX chat_findings_chat_idx ON chat.findings (chat_id, created_at);
CREATE INDEX chat_findings_source_ref_idx ON chat.findings (chat_id, source_ref) WHERE source_ref <> '';

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
      AND (chat.findings.kind = 'attachment' OR chat.findings.source_ref = '' OR chat.findings.author_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid)
      AND (NOT chat.findings.private_read OR (chat.findings.author_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid AND chat.visibility = 'private' AND NOT EXISTS (SELECT 1 FROM identity.shares s WHERE s.object_type = 'chat' AND s.object_id = chat.id)))
  ));

CREATE POLICY chat_findings_insert ON chat.findings
  FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM chat.chats AS chat
    WHERE chat.id = chat_id
      AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
  ));

GRANT SELECT, INSERT ON chat.findings TO upmo_app;

-- What a person allowed for one chat: a tool of change that then runs without a
-- hold while the chat is untainted. One row per chat, person and tool.
CREATE TABLE chat.tool_allowances (
  chat_id uuid NOT NULL REFERENCES chat.chats (id) ON DELETE CASCADE,
  user_id uuid NOT NULL,
  tool text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (chat_id, user_id, tool),
  CONSTRAINT tool_allowances_tool_check CHECK (char_length(tool) BETWEEN 1 AND 120)
);

ALTER TABLE chat.tool_allowances ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat.tool_allowances FORCE ROW LEVEL SECURITY;

CREATE POLICY tool_allowances_owner ON chat.tool_allowances
  FOR ALL
  USING (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.tool_allowances'::regclass))
  )
  WITH CHECK (
    current_user = pg_catalog.pg_get_userbyid(
      (SELECT relowner FROM pg_catalog.pg_class WHERE oid = 'chat.tool_allowances'::regclass))
  );

CREATE POLICY tool_allowances_select ON chat.tool_allowances
  FOR SELECT
  USING (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND identity.may('view', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  );

CREATE POLICY tool_allowances_insert ON chat.tool_allowances
  FOR INSERT
  WITH CHECK (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND NOT chat.tainted
        AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  );

CREATE POLICY tool_allowances_delete ON chat.tool_allowances
  FOR DELETE
  USING (
    user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
    AND EXISTS (
      SELECT 1 FROM chat.chats AS chat
      WHERE chat.id = chat_id
        AND identity.may('edit', chat.object_type, chat.owner_user_id, chat.visibility, chat.visibility_team_id, chat.id)
    )
  );

GRANT SELECT, INSERT, DELETE ON chat.tool_allowances TO upmo_app;

-- +goose Down

DROP TABLE IF EXISTS chat.tool_allowances;
DROP FUNCTION IF EXISTS chat.hold_status(uuid, uuid);
DROP TRIGGER IF EXISTS chat_holds_decide_once ON chat.holds;
DROP FUNCTION IF EXISTS chat.decide_hold_once();
DROP FUNCTION IF EXISTS chat.fail_waiting_runs(uuid);
DROP FUNCTION IF EXISTS chat.close_waiting_run(uuid, text, text);
DROP FUNCTION IF EXISTS chat.delete_chat(uuid);
DROP TABLE IF EXISTS chat.findings;
DROP INDEX IF EXISTS chat.chat_findings_source_ref_idx;
DROP TABLE IF EXISTS chat.action_logs;
DROP TABLE IF EXISTS chat.holds;
DROP FUNCTION IF EXISTS chat.redacted_turn_metadata(uuid);
DROP TABLE IF EXISTS chat.turns;
DROP FUNCTION IF EXISTS chat.fail_reply(uuid, int, uuid, text);
DROP FUNCTION IF EXISTS chat.sweep_deltas();
DROP TRIGGER IF EXISTS chat_lines_stream_notify ON chat.lines;
DROP TRIGGER IF EXISTS chat_deltas_stream_notify ON chat.deltas;
DROP FUNCTION IF EXISTS chat.stream_notify();
DROP TABLE IF EXISTS chat.runs;
DROP TABLE IF EXISTS chat.deltas;
DROP TABLE IF EXISTS chat.lines;
DROP TABLE IF EXISTS chat.chats;
DROP FUNCTION IF EXISTS chat.keep_line();
DROP FUNCTION IF EXISTS chat.advance_chat_exposure(uuid, boolean, boolean);
DROP FUNCTION IF EXISTS chat.advance_chat_summary(uuid, uuid, uuid, text, uuid, int);
DROP TRIGGER IF EXISTS chat_keep_chat ON chat.chats;
DROP FUNCTION IF EXISTS chat.keep_chat();
