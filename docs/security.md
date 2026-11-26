# Security

How Upmo limits what agents can do, and how it protects your data. Back to the [README](../README.md).

To report a vulnerability, see [SECURITY.md](../SECURITY.md). To harden your own deployment, see [Data at rest](operations/data-at-rest.md).

## The model

An agent is not trusted. It can be wrong, and it can be tricked by text it reads. So Upmo does not rely on the agent to behave. Rules are code, not prompts.

Each task runs in a sandbox. The sandbox has 3 limits:

- It holds stand-in keys, not real ones.
- It can reach only the sites on the task's list.
- If the run has read untrusted content, any write outside Upmo waits for approval.

## Keys stay hidden

Agents hold stand-in values. The real credential never enters the sandbox.

Sandboxd swaps a stand-in for the real value on the way out. It does this only toward the host that the credential names. Brain never holds a real value (`internal/sandboxd/inject`). If sandboxd cannot resolve a credential that a host needs, it does not open the connection to that host.

Stored provider credentials, password hashes and email addresses are sealed in the database. The worker seals them with `UPMO_MASTER_KEY`. See [Data at rest](operations/data-at-rest.md).

## Least access per task

Each task gets a short list of tools, stand-in keys and a budget.

The egress proxy is the only way out of a sandbox. It checks the host name against the task's allow list. It does not open the TLS traffic. It logs every decision, allowed or blocked (`internal/sandboxd/egress`).

Calls to provider hosts, to URLs that people give, to audit push and to MCP servers refuse private, loopback and link-local addresses. They connect to the checked IP, so a later DNS answer cannot change the target (`internal/platform/netguard`).

## You approve what matters

Payments, emails to customers and record changes wait for you.

One rule is fixed in Go. If a run has read untrusted content and has a tool that writes outside Upmo, every external write waits for approval (`internal/platform/writeguard`).

Untrusted text is wrapped in a marked fence before it enters a prompt. Control sequences are neutralized first, including look-alike characters (`internal/platform/trustfence`).

## Everything is logged

Security-relevant changes are written to an append-only audit log. Each record is linked to the one before it. A verify step walks the chain and reports the first gap or changed record (`internal/platform/audit`). Restore also checks the chain.

## Your data stays yours

Postgres row-level security enforces who can see each row: the owner, the team, the whole workspace or people with an explicit share.

Brain serves requests as a limited database role, `upmo_app`. It cannot change the schema, and some tables block updates for it. See [Configuration](configuration.md#why-2-database-urls).

Sign-in uses passkeys. Passwords and one-time sign-in links are also supported.

## In transit

Postgres URLs must use `sslmode=verify-full`, unless the host is loopback or a single DNS label such as `postgres`. For SMTP, Upmo requires STARTTLS unless the host is loopback or a single DNS label. For calls between services, such as gateway and sandboxd, Upmo refuses a plain `http` URL unless the host is loopback or a single DNS label.

API responses from brain send `Content-Security-Policy: default-src 'none'` and `Referrer-Policy: no-referrer`. Web app pages send a Content-Security-Policy with `default-src 'self'` and `frame-ancestors 'none'`, plus `Referrer-Policy: strict-origin-when-cross-origin`. HSTS is sent on HTTPS.

## Keys and secrets

- Production refuses the development keys and passwords that `compose.yml` embeds. It also refuses any key made of one repeated byte.
- `UPMO_MASTER_KEY` is required on the worker, on `upmo backup`, on `upmo keys`, on `upmo recover`, and on `upmo bootstrap`. Brain, gateway, and sandboxd refuse to start when it is set. Brain does not receive a password hash or a provider credential from the worker.
- To rotate the master key, see [Data at rest](operations/data-at-rest.md#key-loss-and-retirement).

## Not covered yet

Upmo does not detect or remove personal data in what it sends to a model provider. Version 1 does no scrubbing in the gateway. Do not send data to a provider that you may not share with it.
