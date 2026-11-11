-- +goose Up

-- When the account row last changed. The gateway's status notes carry the
-- time their attempt started, and the store drops one whose account changed
-- while the attempt ran, so an edit that reset the status to Not connected
-- is not overwritten by a stale outcome.
ALTER TABLE gateway.provider_accounts
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
GRANT UPDATE (name, plan_kind, base_url, status, checked_at, updated_at) ON gateway.provider_accounts TO upmo_app;

-- +goose Down

REVOKE UPDATE ON gateway.provider_accounts FROM upmo_app;
GRANT UPDATE (name, plan_kind, base_url, status, checked_at) ON gateway.provider_accounts TO upmo_app;
ALTER TABLE gateway.provider_accounts
  DROP COLUMN updated_at;
