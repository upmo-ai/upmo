# Development

How to build, test and change Upmo. Back to the [README](../README.md).

## Use Docker

Do not install Node, Go or pnpm on your machine for this repository. Every `make` target wraps Docker Compose. The tools live in the containers.

```sh
make build    # build the Go module and the web app
make up       # start brain and postgres
make test     # run the tests
make lint     # run the linters
make ci       # run the same checks as CI
```

Run `make help` for every target.

## Web app

Start the web dev server with `docker compose run --rm --service-ports web pnpm dev`. It listens on localhost:5173. More in [web/README.md](../web/README.md).

## Code layout

The Go module is grouped by bounded context under `internal/`. Technical code lives in `internal/platform`. `internal/archtest` checks the import rules.

The public entry point is `pkg/brain`. The web app lives in `web/`.

## Workflows

Name each GitHub workflow and each job in words, in title case. Examples: `CI`, `Release`, `Security Scan`, `Go Tests`. Do not use lowercase IDs as names.

## Before you open a pull request

Run `make ci`. CI runs the same checks. See [CONTRIBUTING.md](../CONTRIBUTING.md) for commit rules and writing style.
