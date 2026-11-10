-- +goose Up

-- A pinned attempt is recorded at position 0, before every link of the route,
-- whose positions start at 1.
ALTER TABLE gateway.ledger DROP CONSTRAINT ledger_position_check;
ALTER TABLE gateway.ledger ADD CONSTRAINT ledger_position_check CHECK (link_position >= 0);

-- +goose Down

-- Pinned rows have no place in the old shape, so they are renumbered to 1
-- rather than dropped.
UPDATE gateway.ledger SET link_position = 1 WHERE link_position = 0;
ALTER TABLE gateway.ledger DROP CONSTRAINT ledger_position_check;
ALTER TABLE gateway.ledger ADD CONSTRAINT ledger_position_check CHECK (link_position > 0);
