-- +goose Up

-- Rename the durable task→sandbox table to the product name.
ALTER TABLE runtime.seats RENAME TO task_sandboxes;
ALTER TABLE runtime.task_sandboxes RENAME CONSTRAINT seats_task_check TO task_sandboxes_task_check;
ALTER TABLE runtime.task_sandboxes RENAME CONSTRAINT seats_sandbox_check TO task_sandboxes_sandbox_check;

GRANT SELECT, INSERT, DELETE ON runtime.task_sandboxes TO upmo_app;
GRANT UPDATE (sandbox_id, expires_at) ON runtime.task_sandboxes TO upmo_app;

-- +goose Down

ALTER TABLE runtime.task_sandboxes RENAME TO seats;
ALTER TABLE runtime.seats RENAME CONSTRAINT task_sandboxes_task_check TO seats_task_check;
ALTER TABLE runtime.seats RENAME CONSTRAINT task_sandboxes_sandbox_check TO seats_sandbox_check;

GRANT SELECT, INSERT, DELETE ON runtime.seats TO upmo_app;
GRANT UPDATE (sandbox_id, expires_at) ON runtime.seats TO upmo_app;
