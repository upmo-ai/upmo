-- +goose Up

-- What the person picked in the composer when they wrote a user line: the
-- agent, the route, and the model pinned as a provider account and a model
-- id. All are optional, and author_id says who wrote the line, so only they
-- are shown the account and agent they picked. author_id is null on lines
-- written before it existed.
-- They are stored so that a retry with nothing new
-- picked uses them again, and so that the history can show them. agent_id has
-- no foreign key yet: agents come in a later step.
ALTER TABLE chat.lines
  ADD COLUMN agent_id uuid,
  ADD COLUMN route text,
  ADD COLUMN pin_account_id uuid,
  ADD COLUMN pin_model text,
  ADD COLUMN author_id uuid;

ALTER TABLE chat.lines
  ADD CONSTRAINT lines_route_check CHECK (route IS NULL OR char_length(route) BETWEEN 1 AND 64),
  ADD CONSTRAINT lines_pin_check CHECK (
    (pin_account_id IS NULL AND pin_model IS NULL)
    OR (pin_account_id IS NOT NULL AND pin_model IS NOT NULL AND char_length(pin_model) BETWEEN 1 AND 120)
  ),
  ADD CONSTRAINT lines_picks_user_check CHECK (
    role = 'user'
    OR (agent_id IS NULL AND route IS NULL AND pin_account_id IS NULL AND pin_model IS NULL AND author_id IS NULL)
  );

-- +goose Down

ALTER TABLE chat.lines
  DROP CONSTRAINT IF EXISTS lines_picks_user_check,
  DROP CONSTRAINT IF EXISTS lines_pin_check,
  DROP CONSTRAINT IF EXISTS lines_route_check,
  DROP COLUMN IF EXISTS author_id,
  DROP COLUMN IF EXISTS pin_model,
  DROP COLUMN IF EXISTS pin_account_id,
  DROP COLUMN IF EXISTS route,
  DROP COLUMN IF EXISTS agent_id;
