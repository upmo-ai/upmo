-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
  CREATE ROLE upmo_app NOLOGIN;
EXCEPTION
  WHEN duplicate_object THEN NULL;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA identity TO upmo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA identity TO upmo_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA identity TO upmo_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA identity TO upmo_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA identity GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO upmo_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA identity GRANT USAGE, SELECT ON SEQUENCES TO upmo_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA identity GRANT EXECUTE ON FUNCTIONS TO upmo_app;
REVOKE ALL ON identity.goose_db_version FROM upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.may(
  action text,
  object_type text,
  owner_user_id uuid,
  visibility text,
  visibility_team_id uuid,
  id uuid
) RETURNS boolean
LANGUAGE sql
STABLE
AS $$
  SELECT
    CASE
      WHEN action = 'create' THEN
        owner_user_id::text = COALESCE(current_setting('upmo.user_id', true), '')
        AND (object_type || ':create') = ANY (string_to_array(COALESCE(current_setting('upmo.allow', true), ''), ','))
      ELSE
        (object_type || ':' || action) = ANY (string_to_array(COALESCE(current_setting('upmo.allow', true), ''), ','))
        AND (
          owner_user_id::text = COALESCE(current_setting('upmo.user_id', true), '')
          OR id::text = ANY (string_to_array(COALESCE(
            CASE
              WHEN action IN ('edit', 'delete') THEN current_setting('upmo.shared_edit', true)
              ELSE current_setting('upmo.shared_view', true)
            END, ''), ','))
          OR (
            action = 'view'
            AND (
              visibility = 'everyone'
              OR (
                visibility = 'team'
                AND visibility_team_id::text = ANY (string_to_array(COALESCE(current_setting('upmo.teams', true), ''), ','))
              )
            )
          )
        )
    END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.may(text, text, uuid, text, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.may(text, text, uuid, text, uuid, uuid) TO upmo_app;

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = CURRENT_USER AND rolsuper) THEN
    EXECUTE format('GRANT upmo_app TO %I WITH INHERIT FALSE, SET TRUE', CURRENT_USER);
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS identity.may(text, text, uuid, text, uuid, uuid);
REVOKE ALL ON ALL TABLES IN SCHEMA identity FROM upmo_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA identity FROM upmo_app;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA identity FROM upmo_app;
REVOKE USAGE ON SCHEMA identity FROM upmo_app;
