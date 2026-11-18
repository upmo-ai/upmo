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

Env files holding keys and passwords: `0600`. Backup archives are written `0600` by `brain backup`.

## What is sealed in the database

Password hashes (`identity.users.password_hash`) and stored provider credentials (`gateway.credentials`) are sealed with `UPMO_MASTER_KEY` today, and person email addresses will be sealed too. Everything else — chat, tasks, knowledge, memory, agents, audit records and other identity display fields — stays in clear text inside Postgres, protected by row security, volume encryption and encrypted backups, because column encryption would break full-text search and vector indexes.

## Backups

Backups are encrypted by default: AES-256-GCM under a random archive key, wrapped by `UPMO_MASTER_KEY` or by a passphrase passed with `--passphrase-env`. `--no-encrypt` only works with `UPMO_ENV=development`. `brain restore --require-encrypted` refuses an unencrypted archive.

A killed backup can leave a plaintext staging directory (`.upmo-backup-*`) next to the output archive; the next backup in that directory removes it. Keep backup output on an encrypted volume all the same.

## Key loss and retirement

An archive wrapped by the master key cannot be restored without that key. A passphrase archive still needs every master key id listed in its `db/meta.json` to open sealed rows, so never retire a previous master key (`UPMO_MASTER_KEY_PREVIOUS_<id>`) while sealed rows still use it. `upmo keys reseal` re-seals rows under the current key; it is not available yet, so until it ships keep every previous key. After a reseal, take a fresh backup.

## Development keys

`UPMO_ENV` selects the environment: `production` by default, or `development`. Production refuses the keys and passwords `compose.yml` embeds, any all-equal-byte key, empty database passwords, and an empty `UPMO_MASTER_KEY` on brain and gateway. `UPMO_ENV=development` is refused with a public https `UPMO_PUBLIC_URL`.
