# Development

How to build, test and change Upmo. Back to the [README](../README.md).

## Use Docker

Do not install Node, Go or pnpm on your machine for this repository. Every `make` target wraps Docker Compose. The tools live in the containers.

```sh
make build    # build the Go module and the web app
make up       # start the worker, brain and postgres
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

Name each GitHub workflow, job and step in words, in title case. Examples: `CI`, `Release`, `Security Scan`, `Go Tests`, `Check Out`. Do not use lowercase IDs as names.

## Continuous integration

Every pull request and every push to `main` runs the `CI` workflow. The `CI Result` job lists every other job and fails when one fails or is canceled. It is the only check the `main` ruleset requires, so a new job must be added to its `needs` list.

The `Changes` job runs first and turns on only the jobs a change needs. Go changes (`cmd/`, `internal/`, `pkg/`, `tools/`, `go.mod`, `go.sum`), files at the repository root and `web/package.json` run the Go jobs, because repolint tests check those files. Changes under `web/` run Web Checks. End-to-End Tests run for changes under `cmd/`, `internal/`, `pkg/`, `web/`, `e2e/` or `deploy/` and for `go.mod` or `go.sum`. Container Images run for `deploy/` and Dockerfiles. Docs Check runs for Markdown, HTML, `docs/`, `LICENSE` and `lychee.toml`. Canary runs for `scripts/`, and Workflow Security for `.github/`. Lint always checks retired words and adds golangci-lint for Go changes and shellcheck for `scripts/`. Vulnerability Scan, Secret Scan and Commit Messages always run, and Dependency Review runs on every pull request. A change to `.github/`, the `Makefile`, `compose.yml` or a root dotfile such as `.golangci.yml`, and the weekly run, turn on every job. `CI Result` counts a skipped job as passed.

Each job writes a summary on the run page, pass or fail. Jobs that run tests with a report add test counts, the names of failed tests and coverage. A failed job adds the last lines of its output. `repolint summary` builds it from the reports in `reports/`.

- **Coverage.** Go unit tests, Go integration tests and the web tests write coverage. Codecov receives it under the flags `go-unit`, `go-integration` and `web`. A Codecov outage never fails a job. Pull requests from forks upload without a token.
- **Security.** Vulnerability Scan (govulncheck, osv-scanner), Secret Scan (gitleaks), Dependency Review (pull requests only, fails on high severity and on GPL-2.0-only, SSPL-1.0 and BUSL-1.1), Workflow Security (actionlint, zizmor) and Container Images (hadolint, then Trivy on the built images, failing on high or critical findings that have a fix). GitHub's default setup runs CodeQL. The `Scorecard` workflow runs OpenSSF Scorecard on `main` and weekly.
- **Local runs.** `make ci` runs every job above except Go Integration Tests, End-to-End Tests, Fuzz Tests and Dependency Review. Those need a Docker socket, a browser, 30 seconds per fuzz target, or the pull request context. Run them with `make test-integration`, `make e2e` and `make fuzz`.

The `main` ruleset requires a pull request, one approval, code owner review, a passing `CI Result` and an up-to-date branch. It blocks force pushes and deletion and keeps history linear.

## Before you open a pull request

Run `make ci`. CI runs the same checks. See [CONTRIBUTING.md](../CONTRIBUTING.md) for commit rules and writing style.
