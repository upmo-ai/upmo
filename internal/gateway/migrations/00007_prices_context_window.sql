-- +goose Up

ALTER TABLE gateway.prices
  ADD COLUMN context_window int NOT NULL DEFAULT 128000,
  ADD CONSTRAINT prices_context_window_check CHECK (context_window > 0);

-- +goose Down

ALTER TABLE gateway.prices
  DROP CONSTRAINT IF EXISTS prices_context_window_check,
  DROP COLUMN IF EXISTS context_window;
