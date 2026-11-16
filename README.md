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

## Provisioning Configuration

Control-plane provisioning under `/api/v1/provision/*` (see `docs/openapi/provision-v1.yaml`) is disabled (`404`) by default unless `UPMO_PROVISION_KEYS` is configured:

- `UPMO_PROVISION_KEYS`: Comma-separated list of `upmo_svc_*` master keys used to derive HMAC-SHA256 verification keys for `svcsig` (`provision` audience) bearer tokens. Supports multiple keys for zero-downtime key rotation.
- `UPMO_PACK_TRUST_KEYS`: Optional comma-separated `key_id=<base64-ed25519-pub>` overrides/additions for verifying signed `upmo.pack/v1` manifests on `PUT /api/v1/provision/packs`.

## Licence and trademark

Code and documentation are under the [GNU Affero General Public License v3.0](LICENSE) (`AGPL-3.0-only`). The Upmo name, caret mark and wordmark are not; see [TRADEMARK.md](TRADEMARK.md).

Third-party fonts and other assets, with their licences, are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Contributing: [CONTRIBUTING.md](CONTRIBUTING.md). Security reports: [SECURITY.md](SECURITY.md).
