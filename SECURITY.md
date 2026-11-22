# Security policy

## Report a vulnerability

Do not open a public issue, pull request or discussion for a security problem.

Email **security@upmo.ai** with:

- what the problem is and what an attacker can do with it,
- the commit or version,
- steps to reproduce, a proof of concept or logs, if you have them.

Once the repository is public, you can also use GitHub private vulnerability reporting: the "Report a vulnerability" button on the Security tab.

## What happens next

- We acknowledge your report within 3 working days.
- We confirm or reject the problem and tell you our plan within 10 working days.
- We fix confirmed problems and publish an advisory. We aim to disclose within 90 days of your report. If a fix ships earlier, we disclose earlier. We agree on the date with you.
- We credit you in the advisory unless you ask us not to.

## Supported versions

There is no release yet. Security fixes go into `main`.

| Version | Supported |
| ------- | --------- |
| `main`  | Yes       |

This table changes when the first release ships.

## Scope

In scope: the code in this repository, the web build and the scripts in this repository.

Out of scope:

- problems in a dependency that do not depend on how Upmo uses it (report those upstream)
- findings that need a compromised host or administrator access to begin with

## Running Upmo securely

How Upmo protects data is in [docs/security.md](docs/security.md). Volume encryption, backup encryption and key handling are in [docs/operations/data-at-rest.md](docs/operations/data-at-rest.md).

## Safe harbor

We will not take legal action against good-faith research under this policy. In return, we ask you to:

- avoid harm to users and their data
- stop testing once you have proof
- not access more data than you need
- give us enough time to fix the problem before you disclose it
