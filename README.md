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
- `gateway` is the model gateway and arrives with its epic.
- `sandboxd` runs sandbox drivers and the egress proxy and arrives with its epic.

## Layout

The Go module is laid out by bounded context under `internal/`, with technical code in `internal/platform` and the import rules checked by `internal/archtest`. The web app lives in `web/`.
