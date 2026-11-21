<p align="center">
  <img src=".github/assets/banner.svg" alt="Upmo">
</p>

[![CI](https://github.com/upmo-ai/upmo/actions/workflows/ci.yml/badge.svg)](https://github.com/upmo-ai/upmo/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/upmo-ai/upmo)](https://goreportcard.com/report/github.com/upmo-ai/upmo)
[![licence: AGPL v3](https://img.shields.io/badge/licence-AGPL_v3-blue)](LICENSE)

Upmo gives your business AI agents that do real work in the tools you already use. One agent per task: invoices, payroll, support tickets, contracts, deadlines. Agents check their own work, and anything important waits for your approval.

This repository is the open core those guarantees are built on: a modular monolith in Go, `brain`, with a React web app and Postgres. It runs on Upmo's managed cloud at upmo.ai, or on your own servers, free. Every guarantee on the site is enforced here, in code you can read.

## Why safe to trust

- **Keys stay hidden.** Agents hold stand-in keys. Upmo adds the real key on the way out, only for services on the task's list. Agents never see your passwords.
- **You give the final approval.** Payments, emails to customers and record changes wait for you.
- **Work is checked.** Every task runs prepare, plan, work, check. The result is checked against your own records before it counts as done.
- **Least access per task.** Each task gets a short list of tools, a stand-in key and a budget. Requests to unknown sites are blocked.
- **Everything is logged.** Every read, write and sent email lands in the log with the time, the cost and the source.
- **Your data stays yours.** Each team sees only its own data. Sign in with a passkey or your company's SSO.

## What agents work with

- **Your tools.** Connectors work inside your accounts, not in a copy of them. Anything with an API can be connected.
- **Your knowledge.** Playbooks, house rules and documents, searched before agents act. Every learned fact shows where it came from, kept apart per team.
- **Clear costs.** Pick a model per agent, see the exact cost of every task, and set budgets that stop spending.

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
- `upmo backup --out <file>` writes one archive holding a consistent Postgres snapshot, the artifacts and knowledge volumes (`UPMO_ARTIFACTS_DIR`, `UPMO_KNOWLEDGE_DIR`) and a manifest, encrypted by default with a key sealed by `UPMO_MASTER_KEY` (see [docs/operations/data-at-rest.md](docs/operations/data-at-rest.md)).
- `upmo restore --in <file>` verifies the archive, rehearses the restore in a throwaway database, then replaces the database and both volumes, and checks the audit chain. An archive older than the schema is migrated forward by restore itself; run `upmo migrate` only if restore reports that the forward migration failed after the data was restored.
- `upmo keys reseal` re-seals every sealed row under the current `UPMO_MASTER_KEY` after a key rotation, in batches, and can be stopped and run again. `--dry-run` reports the counts without changing anything.
- A default archive needs the master key it was sealed with; a `--passphrase-env <VAR>` archive needs its passphrase instead. Restore refuses an archive whose key id is not in the keyring (`UPMO_MASTER_KEY_ID`, default `k1`; after a rotation keep old keys as `UPMO_MASTER_KEY_PREVIOUS_<id>`).
- `GET /version` and `GET /api/version` need no sign-in. They return JSON with `version`, `commit`, `build_time` and `schema_versions`, a map of each schema to its applied migration version.
- Data at rest, volume encryption, backup encryption and key handling are covered in [docs/operations/data-at-rest.md](docs/operations/data-at-rest.md).
- `UPMO_ENV` selects the environment, `production` (the default) or `development`; production refuses the development keys and passwords `compose.yml` embeds.

## Getting help

Questions and problems go to [GitHub Issues](https://github.com/upmo-ai/upmo/issues). Security reports follow [SECURITY.md](SECURITY.md), never a public issue.

## Licence and trademark

Upmo AI is a product of Klassroom.

Code and documentation are under the [GNU Affero General Public License v3.0](LICENSE) (`AGPL-3.0-only`).

The Upmo name, caret mark and wordmark are not; see [TRADEMARK.md](TRADEMARK.md).

Third-party fonts and other assets, with their licences, are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Contributing: [CONTRIBUTING.md](CONTRIBUTING.md). Security reports: [SECURITY.md](SECURITY.md).
