-- +goose Up

ALTER TABLE gateway.prices
  ADD COLUMN context_window int NOT NULL DEFAULT 128000,
  ADD CONSTRAINT prices_context_window_check CHECK (context_window > 0);

GRANT SELECT ON gateway.prices TO upmo_app;
GRANT SELECT ON gateway.price_tiers TO upmo_app;

-- +goose Down

REVOKE SELECT ON gateway.price_tiers FROM upmo_app;
REVOKE SELECT ON gateway.prices FROM upmo_app;

ALTER TABLE gateway.prices
  DROP CONSTRAINT IF EXISTS prices_context_window_check,
  DROP COLUMN IF EXISTS context_window;
