-- Put the e2e person back where e2e/passkeys.mjs expects to find them and
-- start from full throttle buckets. e2e/ensure-brain.sh runs this against the
-- e2e compose project only. The person, their passkeys and their workspace
-- data stay.
DO $$
BEGIN
  UPDATE identity.users
  SET password_hash = NULL,
      passkey_offered_at = NULL
  WHERE lower(email) = 'ada@example.com';
  IF NOT FOUND THEN
    RAISE EXCEPTION 'e2e reset: no identity.users row for ada@example.com';
  END IF;
END
$$;

DELETE FROM identity.throttle;
