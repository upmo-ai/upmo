# Development

How to build, test and change Upmo. Back to the [README](../README.md).

## Use Docker

Do not install Node, Go or pnpm on your machine for this repository. Every `make` target wraps Docker Compose. The tools live in the containers.

```sh
make build    # build the Go module and the web app
make up       # start the worker, brain and postgres
make test     # run the tests
make lint     # run the linters
make hooks    # turn on the git hooks once (commit rules, migrations, secrets)
make ci       # run the same checks as CI
```

Run `make help` for every target.

## Web app

Start the web dev server with `docker compose run --rm --service-ports web pnpm dev`. It listens on localhost:5173. More in [web/README.md](../web/README.md).

## Code layout

The Go module is grouped by bounded context under `internal/`. Technical code lives in `internal/platform`. `internal/archtest` checks the import rules.

The public entry point is `pkg/brain`. The web app lives in `web/`.

## Workflows

Name each GitHub workflow, job and step in words, in title case. Examples: `CI`, `Release`, `Security Scan`, `Go Tests`, `Check Out`. Do not use lowercase IDs as names.

## Continuous integration

Every pull request and every push to `main` runs the `CI` workflow. The `CI Result` job lists every other job and fails when one fails or is canceled. It is the only check the `main` ruleset requires, so a new job must be added to its `needs` list.

The `Changes` job runs first and turns on only the jobs a change needs. Go changes (`cmd/`, `internal/`, `pkg/`, `tools/`, `go.mod`, `go.sum`) run every Go job. The root policy files, `web/package.json` and `.githooks/` run only Go Tests, because repolint and the hook tests in Go Tests read them. Vulnerability Scan runs for Go changes, for the lock files it scans (`web/pnpm-lock.yaml`, `e2e/package-lock.json`, `deploy/markitdown/requirements.txt`) and for `osv-scanner.toml`. Changes under `web/` run Web Checks. End-to-End Tests run for changes under `cmd/`, `internal/`, `pkg/`, `web/`, `e2e/` or `deploy/` and for `go.mod` or `go.sum`. Container Images run for `deploy/` and Dockerfiles. Docs Check runs for Markdown, HTML, `docs/`, `LICENSE` and `lychee.toml`. Canary runs for `scripts/` and `.githooks/`, and Workflow Security for `.github/`. Lint always checks retired words and adds golangci-lint for Go changes and `.golangci.yml`, and shellcheck for `scripts/` and `.githooks/`. Secret Scan and Commit Messages always run, and Dependency Review runs on every pull request. A change to `ci.yml`, `.github/actions/`, the `Makefile` or a root file `Changes` does not map, and the weekly run, turn on every job. A `compose.yml` change does too, unless it only changes `image:` lines. Then each image turns on the jobs that run it, and an image `Changes` does not map turns on every job. `CI Result` counts a skipped job as passed.

Go jobs restore the module cache and their own build cache from the latest push to `main`. Only pushes to `main` save caches, so a pull request never writes a cache that `main` reads. CI runs Go tests with `-count=1`, so a restored cache never replays a test result. The test Postgres in CI runs without durability (`UPMO_PG_DURABLE=off`). Fuzz Tests run four targets at a time with `make -j4 fuzz`. A newer push cancels a pull request run, but every push to `main` runs to the end.

Each job writes a summary on the run page, pass or fail. Jobs that run tests with a report add test counts, the names of failed tests and coverage. A failed job adds the last lines of its output. `repolint summary` builds it from the reports in `reports/`.

- **Coverage.** Go unit tests, Go integration tests and the web tests write coverage. Codecov receives it under the flags `go-unit`, `go-integration` and `web`. A Codecov outage never fails a job. Pull requests from forks upload without a token.
- **Security.** Vulnerability Scan (govulncheck, osv-scanner), Secret Scan (gitleaks), Dependency Review (pull requests only, fails on high severity and on GPL-2.0-only, SSPL-1.0 and BUSL-1.1), Workflow Security (actionlint, zizmor) and Container Images (hadolint, then Trivy on the built images, failing on high or critical findings that have a fix). GitHub's default setup runs CodeQL. The `Scorecard` workflow runs OpenSSF Scorecard on `main` and weekly.
- **Local runs.** `make ci` runs every job above except Go Integration Tests, End-to-End Tests, Fuzz Tests and Dependency Review. Those need a Docker socket, a browser, 30 seconds per fuzz target, or the pull request context. Run them with `make test-integration`, `make e2e` and `make fuzz`.

The `main` ruleset requires a pull request, one approval, code owner review, a passing `CI Result` and an up-to-date branch. It blocks force pushes and deletion and keeps history linear.

Commit Messages checks every commit message on the branch (subject and body), refuses new migration files, and on a pull request checks the title, which becomes the squash commit subject on `main`.

## Before you open a pull request

Run `make hooks` once, then `make ci`. CI runs the same checks. See [CONTRIBUTING.md](../CONTRIBUTING.md) for commit rules and writing style.
