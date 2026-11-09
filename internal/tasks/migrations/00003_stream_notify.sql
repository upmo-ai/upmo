-- +goose Up

-- A task event or a chat line announces itself to the stream hub of every
-- brain process. The payload carries ids only: the hub reads the rows. NOTIFY
-- is queued in the writing transaction and sent when it commits, so a
-- rollback sends nothing. It is not durable: the hub replays from the tables
-- after a reconnect and on a timer.

-- +goose StatementBegin
CREATE FUNCTION tasks.stream_notify()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  PERFORM pg_notify('task_stream', json_build_object('task', NEW.task_id, 'id', NEW.id)::text);
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER task_events_stream_notify
  AFTER INSERT ON tasks.task_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.stream_notify();

CREATE TRIGGER chat_events_stream_notify
  AFTER INSERT ON tasks.chat_events
  FOR EACH ROW
  EXECUTE FUNCTION tasks.stream_notify();

-- +goose Down

DROP TRIGGER IF EXISTS chat_events_stream_notify ON tasks.chat_events;
DROP TRIGGER IF EXISTS task_events_stream_notify ON tasks.task_events;
DROP FUNCTION IF EXISTS tasks.stream_notify();
