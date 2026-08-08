-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION identity.keep_joined_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  -- Two owner changes already hold row locks, so a table lock at commit
  -- deadlocks them. This lock serialises the check instead.
  PERFORM pg_advisory_xact_lock(7480199);
  IF EXISTS (SELECT 1 FROM identity.users)
     AND NOT EXISTS (
       SELECT 1
       FROM identity.users AS person
       JOIN identity.roles AS role ON role.id = person.role_id
       WHERE role.key = 'owner'
         AND person.joined_at IS NOT NULL
         AND person.disabled_at IS NULL
     ) THEN
    RAISE EXCEPTION 'install has no joined owner';
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER users_keep_joined_owner ON identity.users;

CREATE CONSTRAINT TRIGGER users_keep_joined_owner
AFTER INSERT OR DELETE OR UPDATE OF role_id, joined_at, disabled_at ON identity.users
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION identity.keep_joined_owner();

CREATE INDEX shares_object ON identity.shares (object_type, object_id);

ALTER TABLE identity.users
  ADD CONSTRAINT users_name_check CHECK (name <> '');

ALTER TABLE identity.sign_in_links
  ADD COLUMN id uuid NOT NULL DEFAULT uuidv7();

CREATE UNIQUE INDEX sign_in_links_id ON identity.sign_in_links (id);

CREATE INDEX throttle_updated ON identity.throttle (updated_at);

-- +goose Down

DROP INDEX IF EXISTS identity.throttle_updated;
DROP INDEX IF EXISTS identity.sign_in_links_id;
ALTER TABLE identity.sign_in_links DROP COLUMN IF EXISTS id;
ALTER TABLE identity.users DROP CONSTRAINT IF EXISTS users_name_check;
DROP INDEX IF EXISTS identity.shares_object;

DROP TRIGGER users_keep_joined_owner ON identity.users;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION identity.keep_joined_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM identity.users)
     AND NOT EXISTS (
       SELECT 1
       FROM identity.users AS person
       JOIN identity.roles AS role ON role.id = person.role_id
       WHERE role.key = 'owner'
         AND person.joined_at IS NOT NULL
         AND person.disabled_at IS NULL
     ) THEN
    RAISE EXCEPTION 'install has no joined owner';
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER users_keep_joined_owner
AFTER INSERT OR UPDATE OR DELETE ON identity.users
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION identity.keep_joined_owner();
