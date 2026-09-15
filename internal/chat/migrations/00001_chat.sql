-- +goose Up

CREATE TABLE chat.chats (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  object_type text NOT NULL DEFAULT 'chat',
  owner_user_id uuid NOT NULL,
  visibility text NOT NULL,
  visibility_team_id uuid,
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
  PRIMARY KEY (chat_id, seq),
  CONSTRAINT lines_seq_check CHECK (seq > 0),
  CONSTRAINT lines_body_check CHECK (char_length(body) BETWEEN 1 AND 4000)
);

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

-- +goose Down

DROP TABLE IF EXISTS chat.lines;
DROP TABLE IF EXISTS chat.chats;
DROP FUNCTION IF EXISTS chat.keep_line();
