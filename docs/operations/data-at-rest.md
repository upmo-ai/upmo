# Data at rest

This note says what Upmo protects on disk, and what the operator has to provide. The app does not encrypt its own data volumes and does not detect whether a disk is encrypted; volume encryption belongs to the host or the cloud.

## Live volumes

Encrypt the block volumes underneath Upmo: LUKS2 on a machine you control, or a provider-encrypted block volume in the cloud. This must cover:

- the three compose volumes `upmo-pgdata`, `artifacts-data` (`UPMO_ARTIFACTS_DIR`) and `knowledge-data` (`UPMO_KNOWLEDGE_DIR`),
- Postgres WAL and temp files, which live inside the same `upmo-pgdata` volume,
- every directory backups are written to.

## Swap

Encrypted, or off.

## File permissions

Env files holding keys and passwords: `0600`. Backup archives are written `0600` by `upmo backup`.

## What is sealed in the database

Password hashes (`identity.users.password_hash`), stored provider credentials (`gateway.credentials`) and person email addresses (`identity.users.email`, each with its keyed index hash) are sealed with `UPMO_MASTER_KEY`. Everything else — chat, tasks, knowledge, memory, agents, audit records and other identity display fields — stays in clear text inside Postgres, protected by row security, volume encryption and encrypted backups, because column encryption would break full-text search and vector indexes.

## Backups

Backups are encrypted by default: AES-256-GCM under a random archive key, wrapped by `UPMO_MASTER_KEY` or by a passphrase passed with `--passphrase-env`. `--no-encrypt` only works with `UPMO_ENV=development`. `upmo restore --require-encrypted` refuses an unencrypted archive.

A killed backup can leave a plaintext staging directory (`.upmo-backup-*`) next to the output archive; the next backup in that directory removes it. Keep backup output on an encrypted volume all the same.

## Key loss and retirement

An archive wrapped by the master key cannot be restored without that key. A passphrase archive still needs every master key id listed in its `db/meta.json` to open sealed rows, so never retire a previous master key (`UPMO_MASTER_KEY_PREVIOUS_<id>`) while sealed rows still use it. To retire a key:

1. Set the new key as `UPMO_MASTER_KEY` with a new `UPMO_MASTER_KEY_ID`, keep the old one as `UPMO_MASTER_KEY_PREVIOUS_<old id>`, restart brain and gateway.
2. `upmo keys reseal --dry-run` to see the counts; then `upmo keys reseal`. It re-seals provider keys and connector credentials (`gateway.credentials`), password hashes and email addresses (and their index hashes) under the current key in batches, records `keys.reseal` audit events, and can be stopped and run again.
3. Rows sealed with a key that is not loaded are left alone and make the command exit non-zero; the brain also warns at start-up and exports `upmo_sealed_rows_unknown_key{store}`.
4. When it exits 0, take a fresh backup, then remove `UPMO_MASTER_KEY_PREVIOUS_<old id>` and restart. Archives wrapped by or listing the old key still need it to restore: keep the old key with those archives.

## Development keys

`UPMO_ENV` selects the environment: `production` by default, or `development`. Brain, gateway, and secret-using `upmo` commands require `UPMO_MASTER_KEY` in every environment. Production also refuses the keys and passwords `compose.yml` embeds, any all-equal-byte key, and empty database passwords. `UPMO_ENV=development` is refused with a public https `UPMO_PUBLIC_URL`.
