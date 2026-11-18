-- Put the e2e person back where e2e/passkeys.mjs expects to find them and
-- start from full throttle buckets. e2e/ensure-brain.sh runs this against the
-- e2e compose project only. The person, their passkeys and their workspace
-- data stay. The address is sealed, so the e2e person is found as the single
-- bootstrapped owner.
DO $$
BEGIN
  IF (SELECT count(*)
      FROM identity.users AS person
      JOIN identity.roles AS role ON role.id = person.role_id
      WHERE role.key = 'owner') <> 1 THEN
    RAISE EXCEPTION 'e2e reset: expected exactly one owner';
  END IF;
  UPDATE identity.users
  SET password_hash = NULL,
      passkey_offered_at = NULL
  WHERE id = (SELECT person.id FROM identity.users AS person JOIN identity.roles AS role ON role.id = person.role_id WHERE role.key = 'owner');
  IF NOT FOUND THEN
    RAISE EXCEPTION 'e2e reset: no identity.users row for the owner';
  END IF;
END
$$;

DELETE FROM identity.throttle;
