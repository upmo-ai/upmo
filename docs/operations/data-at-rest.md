# Data at rest

Upmo does not encrypt its own data volumes. It also cannot tell whether a disk is encrypted. Volume encryption is the job of the host or the cloud.

## Live volumes

Encrypt the block volumes under Upmo. Use LUKS2 on a machine you control. In the cloud, use an encrypted block volume from your provider.

Cover all of these:

- the 3 compose volumes: `upmo-pgdata`, `artifacts-data` (`UPMO_ARTIFACTS_DIR`) and `knowledge-data` (`UPMO_KNOWLEDGE_DIR`)
- Postgres WAL and temp files, which live inside `upmo-pgdata`
- every directory that backups are written to

## Swap

Encrypt swap, or turn it off.

## File permissions

Set env files that hold keys or passwords to `0600`. `upmo backup` writes archives as `0600`.

## What is sealed in the database

These values are sealed with `UPMO_MASTER_KEY`:

- password hashes (`identity.users.password_hash`)
- stored provider credentials (`gateway.credentials`)
- email addresses (`identity.users.email`), each with its keyed index hash

Credentials, password hashes and email addresses use a per-row data key wrapped by the master key. Only `upmo worker` wraps and unwraps those data keys. Brain seals a new password hash under a data key the worker wraps, and asks the worker to check a password. Brain never opens a stored password hash, and its database role cannot read the column.

Brain seals a new email address the same way, under a data key the worker wraps for the address's index hash. Brain asks the worker for every index hash, so only the worker holds the index key. Brain never opens a stored address: the worker opens one only for a proof, such as a live session or an unused sign-in link, and brain's database role cannot read the column.

Everything else stays in clear text inside Postgres. That includes chat, tasks, knowledge, memory, agents, audit records and other identity display fields. Row security, volume encryption and encrypted backups protect them. Column encryption would break full-text search and vector indexes.

## Backups

Backups are encrypted by default. Each archive gets a random key and uses AES-256-GCM. That key is wrapped by `UPMO_MASTER_KEY`, or by a passphrase passed with `--passphrase-env`.

`--no-encrypt` works only with `UPMO_ENV=development`. `upmo restore --require-encrypted` refuses an unencrypted archive.

If a backup is killed, it can leave a plain staging directory (`.upmo-backup-*`) next to the archive. The next backup in that directory removes it. Still, keep backup output on an encrypted volume.

## Key loss and retirement

You cannot restore an archive wrapped by the master key without that key.

A passphrase archive still needs every master key id listed in its `db/meta.json`. It uses them to open sealed rows. So never retire a previous master key (`UPMO_MASTER_KEY_PREVIOUS_<id>`) while sealed rows still use it.

To retire a key:

1. On the worker, set the new key as `UPMO_MASTER_KEY` with a new `UPMO_MASTER_KEY_ID`. Keep the old key as `UPMO_MASTER_KEY_PREVIOUS_<old id>`. Restart the worker.
2. Run `upmo keys reseal --dry-run` to see the counts. Then run `upmo keys reseal`.
   - It seals these again under the current key, in batches: stored provider credentials (`gateway.credentials`), password hashes and email addresses, each under a new data key. An email address also gets a new index hash.
   - It records `keys.reseal` audit events.
   - You can stop it and run it again.
3. A row sealed with a key that is not loaded is left alone. The command then exits with a non-zero code. The worker also warns at start-up and exports `upmo_sealed_rows_unknown_key{store}`.
4. When the command exits with 0, take a fresh backup. Then remove `UPMO_MASTER_KEY_PREVIOUS_<old id>` and restart.

Old archives still need the old key to restore. Keep the old key with those archives.

## Development keys

`UPMO_ENV` is `production` by default. The other value is `development`.

The worker and the `upmo` commands that use secrets need `UPMO_MASTER_KEY` in every environment. Run `upmo backup`, `restore`, `keys`, `recover` and `bootstrap` where the master key is set. In compose, that is the worker container. Brain, gateway and sandboxd refuse to start when it is set.

Production refuses:

- the keys and passwords that `compose.yml` embeds
- any key made of one repeated byte
- empty database passwords

`UPMO_ENV=development` is refused when `UPMO_PUBLIC_URL` is a public https URL.
