-- +goose Up

-- A chat remembers when it began, and each line remembers who wrote it, when,
-- and what it cost. uuidv7() is volatile, so the rewrite this forces gives
-- every existing line its own id; now() is stable, so they all carry the one
-- migration instant and become user lines, which carry nothing else.
ALTER TABLE chat.chats
  ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE chat.lines
  ADD COLUMN id uuid NOT NULL DEFAULT uuidv7(),
  ADD COLUMN role text NOT NULL DEFAULT 'user',
  ADD COLUMN at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN model text,
  ADD COLUMN tokens int NOT NULL DEFAULT 0,
  ADD COLUMN cost_micro_usd bigint;

ALTER TABLE chat.lines
  ADD CONSTRAINT lines_id_key UNIQUE (id),
  ADD CONSTRAINT lines_role_check CHECK (role IN ('user', 'assistant')),
  ADD CONSTRAINT lines_tokens_check CHECK (tokens >= 0),
  ADD CONSTRAINT lines_cost_check CHECK (cost_micro_usd IS NULL OR cost_micro_usd >= 0),
  ADD CONSTRAINT lines_user_bare_check CHECK (
    role <> 'user'
    OR (model IS NULL AND tokens = 0 AND cost_micro_usd IS NULL)
  ),
  ADD CONSTRAINT lines_assistant_model_check CHECK (
    role <> 'assistant'
    OR model IS NOT NULL
  );

-- +goose Down

ALTER TABLE chat.lines
  DROP CONSTRAINT IF EXISTS lines_assistant_model_check,
  DROP CONSTRAINT IF EXISTS lines_user_bare_check,
  DROP CONSTRAINT IF EXISTS lines_cost_check,
  DROP CONSTRAINT IF EXISTS lines_tokens_check,
  DROP CONSTRAINT IF EXISTS lines_role_check,
  DROP CONSTRAINT IF EXISTS lines_id_key,
  DROP COLUMN IF EXISTS cost_micro_usd,
  DROP COLUMN IF EXISTS tokens,
  DROP COLUMN IF EXISTS model,
  DROP COLUMN IF EXISTS at,
  DROP COLUMN IF EXISTS role,
  DROP COLUMN IF EXISTS id;

ALTER TABLE chat.chats
  DROP COLUMN IF EXISTS created_at;
