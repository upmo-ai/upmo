-- +goose Up

-- A reply to a user line is an assistant line. It says whether it finished
-- (done) or broke off (failed), which user line it answers, and on a failed
-- line which models were tried and why each failed. feed_id is the id the
-- stream cursor goes by. A reply streams under the line id chosen before its
-- first delta, so that id sorts before the deltas; feed_id is made when the
-- line is stored, after every delta of the reply, so a client holding the
-- last delta still receives the line that follows it.
ALTER TABLE chat.lines
  ADD COLUMN status text NOT NULL DEFAULT 'done',
  ADD COLUMN reply_to uuid,
  ADD COLUMN attempts jsonb,
  ADD COLUMN feed_id uuid NOT NULL DEFAULT uuidv7();

ALTER TABLE chat.lines
  DROP CONSTRAINT lines_body_check,
  DROP CONSTRAINT lines_user_bare_check,
  ADD CONSTRAINT lines_feed_id_key UNIQUE (feed_id),
  ADD CONSTRAINT lines_reply_to_fkey FOREIGN KEY (reply_to) REFERENCES chat.lines (id),
  ADD CONSTRAINT lines_status_check CHECK (status IN ('done', 'failed')),
  ADD CONSTRAINT lines_body_check CHECK (
    char_length(body) <= CASE role WHEN 'user' THEN 4000 ELSE 32000 END
    AND (char_length(body) >= 1 OR (role = 'assistant' AND status = 'failed'))
  ),
  ADD CONSTRAINT lines_user_bare_check CHECK (
    role <> 'user'
    OR (model IS NULL AND tokens = 0 AND cost_micro_usd IS NULL
        AND status = 'done' AND reply_to IS NULL AND attempts IS NULL)
  ),
  ADD CONSTRAINT lines_attempts_check CHECK (status = 'failed' OR attempts IS NULL);

CREATE INDEX lines_reply_to_idx ON chat.lines (reply_to) WHERE reply_to IS NOT NULL;

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

-- The safety net for a reply whose job died for good: deltas older than two
-- hours. A reply lives five minutes and River rescues a stuck job after ten,
-- so nothing a live or a rescued reply needs is that old. The function takes
-- no age, so a caller cannot widen what it deletes. It runs without a person,
-- so the owner of the table, which is the role that runs the function, is let
-- past row security for those rows and no others. That holds for an owner that
-- is neither a superuser nor BYPASSRLS, since FORCE applies to an owner too.
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

-- A user line must end with an assistant line. When the person who wrote it
-- can no longer write to the chat, or the job could not start, no write of
-- theirs can store that line, so this stores it: a failed reply, empty or
-- with the text the deltas of that chat hold under the reply id, and it
-- deletes those deltas and nothing else. why is access or unwritten.
--
-- It has no caller to check, since it runs for a person whose access is gone,
-- so it asks for what only a real job has: a user line that exists and has no
-- done reply, and a reply job still waiting or running for that very line and
-- reply id. It answers stored, answered, or none, which is no such line or
-- job. It reads past row security as the owner, like the other functions of
-- this kind, so it trusts the owner of the schema to be a role that does.
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

-- +goose Down

DROP FUNCTION IF EXISTS chat.fail_reply(uuid, uuid, text);
DROP FUNCTION IF EXISTS chat.sweep_deltas();
DROP TRIGGER IF EXISTS chat_lines_stream_notify ON chat.lines;
DROP TRIGGER IF EXISTS chat_deltas_stream_notify ON chat.deltas;
DROP FUNCTION IF EXISTS chat.stream_notify();
DROP TABLE IF EXISTS chat.deltas;

-- 00002 allows no empty line and none above 4000 characters. A failed reply
-- may be empty and a reply may be longer, and lines are append-only, so
-- going back drops the failed replies and cuts the long ones.
ALTER TABLE chat.lines DISABLE TRIGGER chat_keep_line;
DELETE FROM chat.lines WHERE status = 'failed';
UPDATE chat.lines SET body = left(body, 4000) WHERE char_length(body) > 4000;
ALTER TABLE chat.lines ENABLE TRIGGER chat_keep_line;

DROP INDEX IF EXISTS chat.lines_reply_to_idx;

ALTER TABLE chat.lines
  DROP CONSTRAINT IF EXISTS lines_attempts_check,
  DROP CONSTRAINT IF EXISTS lines_user_bare_check,
  DROP CONSTRAINT IF EXISTS lines_body_check,
  DROP CONSTRAINT IF EXISTS lines_status_check,
  DROP CONSTRAINT IF EXISTS lines_reply_to_fkey,
  DROP CONSTRAINT IF EXISTS lines_feed_id_key,
  DROP COLUMN IF EXISTS feed_id,
  DROP COLUMN IF EXISTS attempts,
  DROP COLUMN IF EXISTS reply_to,
  DROP COLUMN IF EXISTS status;

ALTER TABLE chat.lines
  ADD CONSTRAINT lines_body_check CHECK (char_length(body) BETWEEN 1 AND 4000),
  ADD CONSTRAINT lines_user_bare_check CHECK (
    role <> 'user'
    OR (model IS NULL AND tokens = 0 AND cost_micro_usd IS NULL)
  );
