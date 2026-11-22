# Contributing

Read this file before you open an issue or a pull request.

## Report a bug or request a feature

Use the [GitHub issue tracker](https://github.com/upmo-ai/upmo/issues). Check for an existing issue first.

Include:

- the steps to reproduce the problem
- the commit or version
- your environment

## Open a pull request

1. Sign the [Contributor License Agreement](CLA.md). CLA Assistant asks you once, on your first pull request. You keep the copyright in your work. The agreement lets the maintainer ship it under the AGPL and in the hosted Upmo service.
2. For a big change, open an issue first and agree on the plan.
3. Fork the repository and make your change.
4. Run `make ci`. CI runs the same checks. See [docs/development.md](docs/development.md) for setup.
5. Open the pull request and watch CI.

Internal changes reference their task ID (`UPM-n`) in the pull request. As an outside contributor, reference your issue instead.

## Commits

- Use [Conventional Commits](https://www.conventionalcommits.org): `<type>[optional scope]: <description>`. Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `chore`, `build`, `ci`, `revert`.
- Write the subject in lower case. Keep it to 72 characters. Do not end it with a period. The body says why.
- Do not add a `Signed-off-by` line. The CLA already covers your rights.
- Do not add a license header to each file. `LICENSE`, the license section of the README and the `license` field of `web/package.json` carry the notice.

## Writing style

Our readers often read English as a second language. Write for them.

- Use short sentences. One idea each.
- Say the point first, then the reason.
- Use common words. Write "use", not "utilize".
- Give an example instead of an adjective.
- Do not use idioms or metaphors.
- Do not use em dashes or emoji.
- Write numbers as digits. Write dates like 11 September 2026.
- Use US spelling.
- Use sentence case for headings.

Use one word for one concept. Follow the glossary in the design system: [design.upmo.dev](https://design.upmo.dev).

The `repolint terms` check fails the build on a retired word. Fix it by rewording. Do not skip the file.

## Code of Conduct

Read the [Code of Conduct](CODE_OF_CONDUCT.md). It applies to all project spaces.

## Report a security issue

Do not open a public issue, pull request or discussion. Email **security@upmo.ai**. See [SECURITY.md](SECURITY.md).

## License

Code and documentation are under the [GNU Affero General Public License v3.0](LICENSE). See [Open a pull request](#open-a-pull-request) for the CLA.
