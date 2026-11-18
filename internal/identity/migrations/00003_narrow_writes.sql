-- +goose Up

-- Narrow upmo_app write privileges on identity tables.
-- Secret, role, and disablement columns move behind proof-carrying definer
-- functions; direct INSERT on users and api_tokens and DELETE on users are
-- revoked; future tables receive no default write privileges.

REVOKE UPDATE ON identity.users, identity.sessions, identity.api_tokens FROM upmo_app;
GRANT UPDATE (password_hash, webauthn_handle, joined_at, passkey_offered_at) ON identity.users TO upmo_app;
GRANT UPDATE (last_seen_at, sign_in_method, signed_in_at) ON identity.sessions TO upmo_app;
GRANT UPDATE (last_used_at) ON identity.api_tokens TO upmo_app;

REVOKE INSERT ON identity.api_tokens, identity.users FROM upmo_app;
REVOKE DELETE ON identity.users FROM upmo_app;

ALTER DEFAULT PRIVILEGES IN SCHEMA identity REVOKE INSERT, UPDATE, DELETE ON TABLES FROM upmo_app;

-- Pure catalog expansion matching domain.SystemPermissions and domain.known.
-- +goose StatementBegin
CREATE FUNCTION identity.role_permissions(role_key text, custom_permissions text[])
RETURNS text[]
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog
AS $$
  SELECT CASE
    WHEN role_key = 'owner' THEN ARRAY[
      'task:create', 'task:view', 'task:edit', 'task:share', 'task:delete',
      'chat:create', 'chat:view', 'chat:edit', 'chat:share', 'chat:delete',
      'folder:create', 'folder:view', 'folder:edit', 'folder:share', 'folder:delete',
      'automation:create', 'automation:view', 'automation:edit', 'automation:share', 'automation:delete',
      'agent:create', 'agent:view', 'agent:edit', 'agent:share', 'agent:delete',
      'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share', 'artifact:delete',
      'members:manage', 'teams:manage', 'roles:manage', 'settings:manage',
      'connectors:manage', 'owners:manage', 'audit:view'
    ]::text[]
    WHEN role_key = 'admin' THEN ARRAY[
      'task:create', 'task:view', 'task:edit', 'task:share', 'task:delete',
      'chat:create', 'chat:view', 'chat:edit', 'chat:share', 'chat:delete',
      'folder:create', 'folder:view', 'folder:edit', 'folder:share', 'folder:delete',
      'automation:create', 'automation:view', 'automation:edit', 'automation:share', 'automation:delete',
      'agent:create', 'agent:view', 'agent:edit', 'agent:share', 'agent:delete',
      'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share', 'artifact:delete',
      'members:manage', 'teams:manage', 'roles:manage', 'settings:manage',
      'connectors:manage', 'audit:view'
    ]::text[]
    WHEN role_key = 'member' THEN ARRAY[
      'task:create', 'task:view', 'task:edit', 'task:share', 'task:delete',
      'chat:create', 'chat:view', 'chat:edit', 'chat:share', 'chat:delete',
      'folder:create', 'folder:view', 'folder:edit', 'folder:share', 'folder:delete',
      'automation:create', 'automation:view', 'automation:edit', 'automation:share', 'automation:delete',
      'agent:create', 'agent:view', 'agent:edit', 'agent:share', 'agent:delete',
      'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share', 'artifact:delete'
    ]::text[]
    WHEN role_key = 'viewer' THEN ARRAY[
      'task:view', 'chat:view', 'folder:view', 'automation:view', 'agent:view', 'artifact:view'
    ]::text[]
    WHEN role_key IS NULL OR role_key = '' THEN COALESCE(
      (
        SELECT pg_catalog.array_agg(p)
        FROM pg_catalog.unnest(COALESCE(custom_permissions, '{}'::text[])) AS p
        WHERE p = ANY (ARRAY[
          'task:create', 'task:view', 'task:edit', 'task:share', 'task:delete',
          'chat:create', 'chat:view', 'chat:edit', 'chat:share', 'chat:delete',
          'folder:create', 'folder:view', 'folder:edit', 'folder:share', 'folder:delete',
          'automation:create', 'automation:view', 'automation:edit', 'automation:share', 'automation:delete',
          'agent:create', 'agent:view', 'agent:edit', 'agent:share', 'agent:delete',
          'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share', 'artifact:delete',
          'members:manage', 'teams:manage', 'roles:manage', 'settings:manage',
          'connectors:manage', 'audit:view'
        ]::text[])
      ),
      '{}'::text[]
    )
    ELSE '{}'::text[]
  END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.role_permissions(text, text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.role_permissions(text, text[]) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.member_set_role(
  actor_token_hash bytea,
  target_user_id uuid,
  target_role_id uuid
)
RETURNS TABLE (old_role_id uuid, new_role_id uuid)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_perms text[];
  v_old_role_id uuid;
  v_current_role_key text;
  v_next_role_key text;
  v_next_perms text[];
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF v_actor_id = target_user_id THEN
    RAISE EXCEPTION 'identity: you cannot do this to yourself' USING ERRCODE = '42501';
  END IF;

  SELECT person.role_id, COALESCE(role.key, '')
  INTO v_old_role_id, v_current_role_key
  FROM identity.users AS person
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE person.id = target_user_id
    AND person.disabled_at IS NULL
  FOR NO KEY UPDATE OF person;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such member' USING ERRCODE = 'P0002';
  END IF;

  SELECT COALESCE(role.key, ''), identity.role_permissions(role.key, role.permissions)
  INTO v_next_role_key, v_next_perms
  FROM identity.roles AS role
  WHERE role.id = target_role_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such role' USING ERRCODE = 'P0002';
  END IF;

  IF NOT ('members:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only owners and admins manage members' USING ERRCODE = '42501';
  END IF;

  IF (v_current_role_key = 'owner' OR v_next_role_key = 'owner')
     AND NOT ('owners:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only an owner can change the owner role' USING ERRCODE = '42501';
  END IF;

  IF v_next_role_key <> 'owner' AND NOT (v_next_perms <@ v_actor_perms) THEN
    RAISE EXCEPTION 'identity: you cannot grant a permission you do not hold' USING ERRCODE = '42501';
  END IF;

  UPDATE identity.users
  SET role_id = target_role_id
  WHERE id = target_user_id;

  old_role_id := v_old_role_id;
  new_role_id := target_role_id;
  RETURN NEXT;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.member_set_role(bytea, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.member_set_role(bytea, uuid, uuid) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.member_disable(
  actor_token_hash bytea,
  target_user_id uuid,
  at timestamptz
)
RETURNS void
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_perms text[];
  v_target_role_key text;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;
  IF at IS NULL THEN
    RAISE EXCEPTION 'identity: disable timestamp required' USING ERRCODE = '22004';
  END IF;

  SELECT person.id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF v_actor_id = target_user_id THEN
    RAISE EXCEPTION 'identity: you cannot do this to yourself' USING ERRCODE = '42501';
  END IF;

  SELECT COALESCE(role.key, '')
  INTO v_target_role_key
  FROM identity.users AS person
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE person.id = target_user_id
    AND person.disabled_at IS NULL
  FOR NO KEY UPDATE OF person;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: no such member' USING ERRCODE = 'P0002';
  END IF;

  IF NOT ('members:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only owners and admins manage members' USING ERRCODE = '42501';
  END IF;

  IF v_target_role_key = 'owner' AND NOT ('owners:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only an owner can change the owner role' USING ERRCODE = '42501';
  END IF;

  DELETE FROM identity.passkeys WHERE user_id = target_user_id;
  DELETE FROM identity.sessions WHERE user_id = target_user_id;
  DELETE FROM identity.team_members WHERE user_id = target_user_id;
  DELETE FROM identity.shares WHERE user_id = target_user_id;
  DELETE FROM identity.webauthn_challenges WHERE user_id = target_user_id;
  DELETE FROM identity.sign_in_links WHERE user_id = target_user_id AND used_at IS NULL;
  DELETE FROM identity.api_tokens WHERE user_id = target_user_id;

  UPDATE identity.users
  SET disabled_at = at,
      password_hash = NULL,
      webauthn_handle = NULL
  WHERE id = target_user_id AND disabled_at IS NULL;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.member_disable(bytea, uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.member_disable(bytea, uuid, timestamptz) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.member_invite(
  actor_token_hash bytea,
  p_email text,              -- sealed value
  p_email_hashes bytea[],    -- index hashes under every loaded key, current key's first
  p_email_hash_key_id text,  -- id of the key that made p_email_hashes[1]
  p_name text,
  p_role_id uuid,
  p_handle bytea,
  p_link_hash bytea,
  p_now timestamptz,
  p_expires timestamptz
)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_perms text[];
  v_target_role_key text;
  v_owner_role_id uuid;
  v_user_id uuid;
  v_disabled boolean;
  v_matches integer;
BEGIN
  -- The address itself is sealed, so only its presence can be checked here;
  -- the caller has already validated the address.
  IF p_email IS NULL OR pg_catalog.btrim(p_email) = ''
     OR p_email_hashes IS NULL OR pg_catalog.cardinality(p_email_hashes) < 1
     OR pg_catalog.octet_length(p_email_hashes[1]) IS DISTINCT FROM 32
     OR p_email_hash_key_id IS NULL OR pg_catalog.btrim(p_email_hash_key_id) = '' THEN
    RAISE EXCEPTION 'identity: a person needs an email address, a name and a role' USING ERRCODE = '23514';
  END IF;

  IF actor_token_hash IS NULL THEN
    PERFORM pg_catalog.pg_advisory_xact_lock(7480198);
    IF EXISTS (SELECT 1 FROM identity.users) THEN
      RAISE EXCEPTION 'identity: someone already exists' USING ERRCODE = '42501';
    END IF;
    SELECT role.id INTO v_owner_role_id FROM identity.roles AS role WHERE role.key = 'owner';
    INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, joined_at, webauthn_handle)
    VALUES (p_email, p_email_hashes[1], p_email_hash_key_id, p_name, v_owner_role_id, p_now, p_now, p_handle)
    RETURNING id INTO v_user_id;

    INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
    VALUES (p_link_hash, v_user_id, 'bootstrap', p_expires);

    RETURN v_user_id;
  END IF;

  SELECT identity.role_permissions(role.key, role.permissions)
  INTO v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  IF p_name IS NULL OR pg_catalog.btrim(p_name) = '' OR p_role_id IS NULL THEN
    RAISE EXCEPTION 'identity: a person needs an email address, a name and a role' USING ERRCODE = '23514';
  END IF;

  SELECT COALESCE(role.key, '')
  INTO v_target_role_key
  FROM identity.roles AS role
  WHERE role.id = p_role_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: a person needs an email address, a name and a role' USING ERRCODE = '23503';
  END IF;

  IF NOT ('members:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only owners and admins manage members' USING ERRCODE = '42501';
  END IF;

  IF v_target_role_key = 'owner' AND NOT ('owners:manage' = ANY (v_actor_perms)) THEN
    RAISE EXCEPTION 'identity: only an owner can change the owner role' USING ERRCODE = '42501';
  END IF;

  -- ON CONFLICT cannot match "any of the hashes", so the row is found first.
  -- A concurrent insert of the same address loses the race as a unique
  -- violation on users_email_hash, which the caller maps the same way.
  -- Rows for the same address under different keys cannot be told apart, so
  -- more than one match is refused rather than picking one.
  PERFORM 1 FROM identity.users AS person
  WHERE person.email_hash = ANY (p_email_hashes)
  FOR UPDATE;
  GET DIAGNOSTICS v_matches = ROW_COUNT;
  IF v_matches > 1 THEN
    RAISE EXCEPTION 'identity: already a member' USING ERRCODE = '23505';
  END IF;

  SELECT person.id, person.disabled_at IS NOT NULL INTO v_user_id, v_disabled
  FROM identity.users AS person
  WHERE person.email_hash = ANY (p_email_hashes)
  FOR UPDATE;
  IF FOUND AND NOT v_disabled THEN
    RAISE EXCEPTION 'identity: already a member' USING ERRCODE = '23505';
  ELSIF FOUND THEN
    UPDATE identity.users
    SET email = p_email, email_hash = p_email_hashes[1], email_hash_key_id = p_email_hash_key_id,
        name = p_name, role_id = p_role_id, invited_at = p_now, joined_at = NULL, disabled_at = NULL,
        webauthn_handle = p_handle, password_hash = NULL, passkey_offered_at = NULL, external_id = NULL
    WHERE id = v_user_id;
  ELSE
    INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, webauthn_handle)
    VALUES (p_email, p_email_hashes[1], p_email_hash_key_id, p_name, p_role_id, p_now, p_handle)
    RETURNING id INTO v_user_id;
  END IF;

  DELETE FROM identity.passkeys WHERE user_id = v_user_id;
  DELETE FROM identity.sessions WHERE user_id = v_user_id;
  DELETE FROM identity.team_members WHERE user_id = v_user_id;
  DELETE FROM identity.shares WHERE user_id = v_user_id;
  DELETE FROM identity.webauthn_challenges WHERE user_id = v_user_id;
  DELETE FROM identity.sign_in_links WHERE user_id = v_user_id AND used_at IS NULL;
  DELETE FROM identity.api_tokens WHERE user_id = v_user_id;

  INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
  VALUES (p_link_hash, v_user_id, 'invite', p_expires);

  RETURN v_user_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.member_invite(bytea, text, bytea[], text, text, uuid, bytea, bytea, timestamptz, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.member_invite(bytea, text, bytea[], text, text, uuid, bytea, bytea, timestamptz, timestamptz) TO upmo_app;

-- +goose StatementBegin
CREATE FUNCTION identity.token_create(
  actor_token_hash bytea,
  p_name text,
  p_token_hash bytea,
  p_scope text[],
  p_created timestamptz,
  p_expires timestamptz
)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
  v_actor_id uuid;
  v_actor_perms text[];
  v_token_id uuid;
  v_clean_name text;
BEGIN
  IF actor_token_hash IS NULL THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  SELECT person.id, identity.role_permissions(role.key, role.permissions)
  INTO v_actor_id, v_actor_perms
  FROM identity.sessions AS session
  JOIN identity.users AS person ON person.id = session.user_id
  JOIN identity.roles AS role ON role.id = person.role_id
  WHERE session.token_hash = actor_token_hash
    AND session.expires_at > pg_catalog.now()
    AND person.disabled_at IS NULL
    AND person.joined_at IS NOT NULL;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity: invalid actor session' USING ERRCODE = '42501';
  END IF;

  v_clean_name := pg_catalog.btrim(COALESCE(p_name, ''));
  IF v_clean_name = '' OR pg_catalog.char_length(v_clean_name) > 64 THEN
    RAISE EXCEPTION 'identity: a token needs a name' USING ERRCODE = '22023';
  END IF;

  IF p_scope IS NULL OR COALESCE(pg_catalog.array_length(p_scope, 1), 0) = 0 THEN
    RAISE EXCEPTION 'identity: a token cannot hold that permission' USING ERRCODE = '42501';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM pg_catalog.unnest(p_scope) AS s
    WHERE s IS NULL
       OR NOT (s = ANY (ARRAY[
         'task:create', 'task:view', 'task:edit', 'task:share',
         'chat:create', 'chat:view', 'chat:edit', 'chat:share',
         'folder:create', 'folder:view', 'folder:edit', 'folder:share',
         'automation:create', 'automation:view', 'automation:edit', 'automation:share',
         'agent:create', 'agent:view', 'agent:edit', 'agent:share',
         'artifact:create', 'artifact:view', 'artifact:edit', 'artifact:share',
         'audit:view'
       ]::text[]))
       OR NOT (s = ANY (v_actor_perms))
  ) THEN
    RAISE EXCEPTION 'identity: a token cannot hold that permission' USING ERRCODE = '42501';
  END IF;

  IF (SELECT pg_catalog.count(DISTINCT s) FROM pg_catalog.unnest(p_scope) AS s)
     <> pg_catalog.array_length(p_scope, 1) THEN
    RAISE EXCEPTION 'identity: a token cannot hold that permission' USING ERRCODE = '42501';
  END IF;

  INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
  VALUES (v_actor_id, v_clean_name, p_token_hash, p_scope, p_created, p_expires)
  RETURNING id INTO v_token_id;

  RETURN v_token_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION identity.token_create(bytea, text, bytea, text[], timestamptz, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION identity.token_create(bytea, text, bytea, text[], timestamptz, timestamptz) TO upmo_app;

-- +goose Down

DROP FUNCTION IF EXISTS identity.token_create(bytea, text, bytea, text[], timestamptz, timestamptz);
DROP FUNCTION IF EXISTS identity.member_invite(bytea, text, bytea[], text, text, uuid, bytea, bytea, timestamptz, timestamptz);
DROP FUNCTION IF EXISTS identity.member_disable(bytea, uuid, timestamptz);
DROP FUNCTION IF EXISTS identity.member_set_role(bytea, uuid, uuid);
DROP FUNCTION IF EXISTS identity.role_permissions(text, text[]);

ALTER DEFAULT PRIVILEGES IN SCHEMA identity GRANT INSERT, UPDATE, DELETE ON TABLES TO upmo_app;
GRANT DELETE ON identity.users TO upmo_app;
GRANT INSERT ON identity.api_tokens, identity.users TO upmo_app;
GRANT UPDATE ON identity.users, identity.sessions, identity.api_tokens TO upmo_app;
