# Operations

How to run Upmo on your own servers. Back to the [README](../../README.md).

Environment variables are in [Configuration](../configuration.md).

## Services

- `brain` runs every part of the app in one process.
- `gateway` routes model calls and holds provider credentials. It listens on localhost:8082. It builds each chat request in this order: tools, system, messages. Each attempt writes one ledger row.
- `sandboxd` serves the sandbox port, the egress proxy and the credential injector. It listens on localhost:8083. It runs each sandbox as a Docker container through the engine socket. If the engine does not answer, it uses an in-memory stub (`UPMO_SANDBOX_DRIVER`).

## Create the first owner

Run `upmo bootstrap --email <email> --name <name>`. It prints a one-time link. Open the link to register the owner's passkey.

## Back up

`upmo backup` writes one archive. It holds:

- a consistent Postgres snapshot
- the artifacts and knowledge volumes
- a manifest

The archive is encrypted by default. The key is sealed by `UPMO_MASTER_KEY`. See [Data at rest](data-at-rest.md).

By default the file is `upmo-backup-<UTC time>.tar.gz` in the current directory, for example `upmo-backup-20261122T093000Z.tar.gz`. To choose another path, use `--out <file>`.

Backup never overwrites a file. If the file exists, the command stops with an error.

The volumes are read from `UPMO_ARTIFACTS_DIR` and `UPMO_KNOWLEDGE_DIR`. If you did not set them and the default directory does not exist, backup skips that volume. If you set a directory and it does not exist, backup stops with an error.

A default archive needs the master key it was sealed with. An archive made with `--passphrase-env <VAR>` needs that passphrase instead of the master key. Sealed database rows still need the master keys they were sealed with.

## Restore

The `--in <file>` flag is required for `upmo restore`. It does 4 things:

1. It verifies the archive.
2. It rehearses the restore in a throwaway database.
3. It replaces the database and both volumes.
4. It checks the audit chain.

If the archive is older than the schema, restore migrates it forward. Run `upmo migrate` only if restore says the forward migration failed after the data was restored.

Restore refuses an archive whose key id is not in the keyring.

Restore writes the volumes to `UPMO_ARTIFACTS_DIR` and `UPMO_KNOWLEDGE_DIR`, or to their defaults. It creates a directory if it is missing.

## Rotate keys

After you change `UPMO_MASTER_KEY`, run `upmo keys reseal`. It seals every sealed row again under the current key. It works in batches. You can stop it and run it again.

Add `--dry-run` to see the counts without changing anything. Full steps are in [Data at rest](data-at-rest.md#key-loss-and-retirement).

## Check the version

`GET /version` and `GET /api/version` need no sign-in. They return JSON with:

- `version`
- `commit`
- `build_time`
- `schema_versions`: each schema and its applied migration version
