# Upmo

Upmo runs always-on AI agents that carry out tasks for you. They run on Upmo's managed cloud at upmo.ai, or on infrastructure you own with the open-source release from upmo.dev. This repository is the core: a modular monolith in Go, `brain`, with a React web app and Postgres.

## Quick start

You need Docker and nothing else. Every command runs in a container defined in `compose.yml`.

```sh
make up               # start brain and postgres, brain listens on localhost:8080
curl -fsS localhost:8080/healthz
make build test lint  # the same checks CI runs
make down
```

Run `make help` for every target. The web dev server runs with `docker compose run --rm --service-ports web pnpm dev` on localhost:5173.

Copy `.env.example` to `.env` to override settings. `.env` is never committed.

## Services

- `brain` hosts every bounded context in one process.
- `gateway` routes model calls and holds provider credentials. It listens on localhost:8082. Chat is built as tools, then system, then messages. Each attempt writes a ledger row.
- `sandboxd` serves the sandbox port, the egress proxy and the credential injector. It listens on localhost:8083. It runs sandboxes as Docker containers through the engine socket and falls back to an in-memory stub when the engine does not answer (`UPMO_SANDBOX_DRIVER`).

## Layout

The Go module is laid out by bounded context under `internal/`, with technical code in `internal/platform` and the import rules checked by `internal/archtest`. The public composition entrypoint lives in `pkg/brain`. The web app lives in `web/`.

## Operations

- `UPMO_METRICS_ADDR` (`[host]:port`, unset by default) starts a separate listener that serves Prometheus metrics at `GET /metrics`, without authentication. Unset, no metrics listener runs. It must differ from `UPMO_LISTEN_ADDR` (and, for `sandboxd`, `UPMO_PROXY_ADDR` and `UPMO_EDITOR_ADDR`); bind it to loopback or an internal network only. `brain`, `gateway` and `sandboxd` each read it.
- `brain backup --out <file>` writes one archive holding a consistent Postgres snapshot, the artifacts and knowledge volumes (`UPMO_ARTIFACTS_DIR`, `UPMO_KNOWLEDGE_DIR`) and a manifest. The archive is encrypted by default with a key sealed by `UPMO_MASTER_KEY`. `--passphrase-env <VAR>` seals it with the passphrase in that environment variable instead; `--no-encrypt` writes a plain `tar.gz` and is refused unless `UPMO_ENV=development` (see `docs/operations/data-at-rest.md`). It reads `UPMO_DATABASE_URL`.
- `brain restore --in <file>` verifies the archive, rehearses the restore in a throwaway database, then replaces the database and both volumes, and checks the audit chain. `--passphrase-env <VAR>` supplies the passphrase and `--require-encrypted` refuses a plain archive. An archive older than the schema is migrated forward by restore itself; run `brain migrate` only if restore reports that the forward migration failed after the data was restored.
- `brain keys reseal` re-seals every sealed row — provider keys and connector credentials (`gateway.credentials`), password hashes and email addresses — under the current `UPMO_MASTER_KEY` in batches (`--batch-size`, default 200), recording `keys.reseal` audit events; it can be stopped and run again. `--dry-run` reports the counts without changing anything. Rows sealed with a key that is not loaded are left unchanged and make the command exit non-zero; brain warns about them at start-up and exports `upmo_sealed_rows_unknown_key{store}`. Run it after rotating the master key, then take a fresh backup (see `docs/operations/data-at-rest.md`).
- A default archive needs the master key it was sealed with; a `--passphrase-env` archive needs its passphrase instead. Provider keys and password hashes in the database are sealed values, so every archive, including `--no-encrypt`, needs their key ids in the keyring (`UPMO_MASTER_KEY_ID`, default `k1`; after a rotation keep old keys as `UPMO_MASTER_KEY_PREVIOUS_<id>`). Restore refuses an archive whose key id is not in the keyring.
- `GET /version` and `GET /api/version` need no sign-in. They return JSON with `version`, `commit`, `build_time` and `schema_versions`, a map of each schema to its applied migration version. They report the versions read at startup.

## Provisioning Configuration

Control-plane provisioning under `/api/v1/provision/*` (see `docs/openapi/provision-v1.yaml`) is disabled (`404`) by default unless `UPMO_PROVISION_KEYS` is configured:

- `UPMO_PROVISION_KEYS`: Comma-separated list of `upmo_svc_*` master keys used to derive HMAC-SHA256 verification keys for `svcsig` (`provision` audience) bearer tokens. Supports multiple keys for zero-downtime key rotation.
- `UPMO_PACK_TRUST_KEYS`: Optional comma-separated `key_id=<base64-ed25519-pub>` overrides/additions for verifying signed `upmo.pack/v1` manifests on `PUT /api/v1/provision/packs`.

## Operations

- Data at rest — volume encryption, backup encryption and key handling — is covered in [docs/operations/data-at-rest.md](docs/operations/data-at-rest.md).
- `UPMO_ENV` selects the environment, `production` (the default) or `development`; production refuses the development keys and passwords `compose.yml` embeds.

## Licence and trademark

Code and documentation are under the [GNU Affero General Public License v3.0](LICENSE) (`AGPL-3.0-only`). The Upmo name, caret mark and wordmark are not; see [TRADEMARK.md](TRADEMARK.md).

Third-party fonts and other assets, with their licences, are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Contributing: [CONTRIBUTING.md](CONTRIBUTING.md). Security reports: [SECURITY.md](SECURITY.md).
