# Contributing

Thanks for helping. This file covers how to work on the repository and what a pull request needs.

## Before you start

- Sign the [Contributor License Agreement](CLA.md). CLA Assistant asks you once, on your first pull request. You keep the copyright in your work; the agreement lets the maintainer ship it under the AGPL and in the hosted Upmo service.
- Read the [Code of Conduct](CODE_OF_CONDUCT.md).

## Development runs in Docker

Do not install Node, Go or pnpm on your machine for this repository. Every `make` target wraps Docker Compose, so the toolchain lives in the containers.

```sh
make build    # build the Go module and the web app
make up       # start the stack
make test     # run the tests
make lint     # run the linters
make ci       # run the same checks as CI
```

Run `make ci` before you open a pull request. CI runs the same checks.

## Tasks

Each change links its task ID (`UPM-n`) in the pull request.

## Commits

- Use [Conventional Commits](https://www.conventionalcommits.org): `<type>[optional scope]: <description>`, with types `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `chore`, `build`, `ci`, `revert`. Subject in lower case, 72 characters at most, no trailing period. The body says why.
- The CLA covers your rights in the contribution, so a `Signed-off-by` line is not needed.
- Do not add per-file licence headers. `LICENSE`, the licence section of the README and the `license` field of `web/package.json` carry the notice.

## Writing style

Write plain, direct sentences. Use sentence case for headings. Use no em dashes and no emoji. Use one word for one concept, following the glossary in the design system: [design.upmo.dev](https://design.upmo.dev).
