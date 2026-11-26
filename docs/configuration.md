# Configuration

Copy `.env.example` to `.env` to override settings. `.env` is never committed.

## Environment

### Mandatory

The service does not start if one of these is missing.

| Variable | Needed by | What it does |
| --- | --- | --- |
| `UPMO_DATABASE_URL` | `upmo worker`, `upmo` commands | Postgres URL of the privileged role. Brain HTTP, gateway and sandboxd never read it; sandboxd refuses to start when it is set. |
| `UPMO_DATABASE_APP_URL` | brain | Postgres URL for the `upmo_app` role. |
| `UPMO_DATABASE_RUNNER_URL` | brain | Postgres URL for the `upmo_runner` role, which writes the task runner columns. |
| `UPMO_DATABASE_GATEWAY_URL` | gateway | Postgres URL for the `upmo_gateway` role. |
| `UPMO_PUBLIC_URL` | brain, gateway, `upmo worker`, `upmo` commands | Browser origin, an absolute http(s) URL. |
| `UPMO_MASTER_KEY` | `upmo worker`, brain (until passwords and email move), `upmo` commands that use secrets | Base64 of exactly 32 bytes. Wraps each provider credential's data key, and seals passwords, email and default backup archives. Needed in every environment, including development. Gateway and sandboxd refuse to start when it, or any `UPMO_MASTER_KEY_PREVIOUS_<id>`, is set. |
| `UPMO_WORKER_BRAIN_KEY`, `UPMO_WORKER_GATEWAY_KEY`, `UPMO_WORKER_SANDBOXD_KEY` | `upmo worker` (all three); brain, gateway and sandboxd (their own) | `upmo_svc_worker-brain_`, `upmo_svc_worker-gateway_` or `upmo_svc_worker-sandboxd_` plus base64 of exactly 32 bytes. Each caller signs its calls to the worker's key service with its own. The worker needs all three and checks each. A process given another caller's key stops at start. |
| `UPMO_GATEWAY_KEY` | gateway | `upmo_svc_gateway_` plus base64 of exactly 32 bytes. Gateway checks every call with it. Brain signs gateway calls with it and makes none without it. |
| `UPMO_SANDBOXD_KEY` | sandboxd | `upmo_svc_sandboxd_` plus base64 of exactly 32 bytes. Sandboxd checks every call with it. Brain needs it when `UPMO_SANDBOXD_URL` is set. |
| `UPMO_EDITOR_KEY` | sandboxd | `upmo_edk_` plus base64 of exactly 32 bytes. Brain signs editor links with it and sandboxd checks them. Without it, brain opens no editor. |
| `UPMO_MARKITDOWN_KEY` | markitdown sidecar | `upmo_svc_markitdown_` plus base64 of exactly 32 bytes. The sidecar checks every `/convert` call with it. Brain uses the sidecar only when this key and `UPMO_MARKITDOWN_URL` are set. |

Each process gets only the keys it signs or checks. A key for another service, or the old `upmo_svc_` master, stops the process at start. `UPMO_SERVICE_KEY` and `UPMO_SERVICE_KEY_PREVIOUS` are refused, even when empty.

### Optional

| Variable | Default | What it does |
| --- | --- | --- |
| `UPMO_ENV` | `production` | `production` or `development`. Production refuses the development keys and passwords `compose.yml` embeds. |
| `UPMO_DATABASE_SANDBOX_URL` | unset | Postgres URL sandboxd logs in with, as the `upmo_sandbox` role. Unset opens no database, and every credential read fails closed. |
| `UPMO_WORKER_LISTEN_ADDR` | `:8090` | The `upmo worker` internal listener. It serves `/api/v1/provision/*` and the key service at `/internal/keys/*`. Never publish it on the host. |
| `UPMO_WORKER_URL` | required for brain and gateway | The worker's key service, for brain, gateway and sandboxd. It must be https in production; development allows http on loopback or one DNS label. The process's own worker key is required with it, and the worker must answer at start. Brain and gateway stop without it. Sandboxd may leave it unset until its proxy opens credentials; then it has no worker client. |
| `UPMO_LISTEN_ADDR` | `:8080` (`sandboxd`: `:8083`; `compose.yml` sets gateway to `:8082`) | Listen address. |
| `UPMO_PROXY_ADDR`, `UPMO_EDITOR_ADDR` | `:3128`, `:8084` | `sandboxd` egress proxy and editor addresses. |
| `UPMO_SANDBOX_DRIVER` | `auto` | `auto`, `docker`, `kubernetes` or `stub`. |
| `UPMO_METRICS_ADDR` | unset | `[host]:port` for a separate listener that serves Prometheus metrics at `GET /metrics`. When unset, no metrics listener runs. Must differ from `UPMO_LISTEN_ADDR` (and, for `sandboxd`, `UPMO_PROXY_ADDR` and `UPMO_EDITOR_ADDR`). Bind it to loopback or an internal network only. |
| `UPMO_METRICS_TOKEN` | unset | `upmo_mtr_` plus base64 of at least 32 bytes. Required when `UPMO_METRICS_ADDR` is set. Scrapers send it as `Authorization: Bearer <token>`. |
| `UPMO_RUN_KEY` | unset | `upmo_rnk_` plus base64 of exactly 32 bytes. Brain signs connector run tokens with it. |
| `UPMO_GATEWAY_KEY_PREVIOUS`, `UPMO_SANDBOXD_KEY_PREVIOUS`, `UPMO_MARKITDOWN_KEY_PREVIOUS`, `UPMO_EDITOR_KEY_PREVIOUS`, `UPMO_RUN_KEY_PREVIOUS`, `UPMO_WORKER_*_KEY_PREVIOUS` | unset | The old key during a rotation. It is still checked but never signs. |
| `UPMO_ARTIFACTS_DIR` | `/var/upmo/artifacts` | Absolute path of the artifacts volume. Brain creates it at start. Backups include it. |
| `UPMO_KNOWLEDGE_DIR` | `/var/upmo/knowledge` | Absolute path of the knowledge volume. Brain creates it at start. Backups include it. |
| `UPMO_MASTER_KEY_ID` | `k1` | ID of the current master key. |
| `UPMO_MASTER_KEY_PREVIOUS_<id>` | | Old master keys kept after a rotation, so older data and archives still open. |

This list is not complete. Other optional settings exist, such as `UPMO_TRUSTED_PROXIES`, `UPMO_WEBAUTHN_RP_ID` and `UPMO_SMTP_*`. If you set any `UPMO_SMTP_` value, `UPMO_SMTP_HOST` and `UPMO_SMTP_FROM` become required.

### Why several database URLs

Every process connects to the same Postgres database, each as its own role.

- `UPMO_DATABASE_URL` is the privileged role: a superuser or `BYPASSRLS` role that owns the tables. Only `upmo worker` and the `upmo` commands (`migrate`, `backup`, `restore`, `keys`, `bootstrap`, `recover`) use it. The worker runs migrations, sets the other roles' passwords, runs the jobs that read every person's rows, and serves provisioning.
- `UPMO_DATABASE_APP_URL` is the role that serves web requests in brain. It is granted only what the app needs. Tables use row-level security. A request cannot change the schema or read another team's rows.
- `UPMO_DATABASE_RUNNER_URL` is the role brain's task runner writes check results, plans, spend and taint with. `upmo_app` may not write those columns.
- `UPMO_DATABASE_GATEWAY_URL` is the role gateway dispatches with. It reads routes, prices and sealed credentials, and writes the ledger and spend.
- `UPMO_DATABASE_SANDBOX_URL` is the role sandboxd reads one bound credential with, only while its task runs.

Each role URL must log in as its own role (`upmo_app`, `upmo_runner`, `upmo_gateway`, `upmo_sandbox`) and must include a password. You do not create the roles or set their passwords yourself. The migrations create them `NOLOGIN`. The worker sets each password from its URL on every start; a role whose URL the worker does not get stays `NOLOGIN`. Brain, gateway and sandboxd refuse to start on a role that is a superuser, has `BYPASSRLS`, or owns a table. Start the worker first.

To rotate a password, change it in the role's URL for the worker and for the process that uses it, then restart the worker before that process.

### Generating the values

```sh
# a URL-safe password for the app role (hex needs no escaping in a URL)
openssl rand -hex 24

# UPMO_MASTER_KEY: base64 of exactly 32 bytes
openssl rand -base64 32

# One key per service, each from its own random bytes. Give a key only to the
# processes listed for it above.
echo "UPMO_GATEWAY_KEY=upmo_svc_gateway_$(openssl rand -base64 32)"
echo "UPMO_SANDBOXD_KEY=upmo_svc_sandboxd_$(openssl rand -base64 32)"
echo "UPMO_MARKITDOWN_KEY=upmo_svc_markitdown_$(openssl rand -base64 32)"
echo "UPMO_EDITOR_KEY=upmo_edk_$(openssl rand -base64 32)"
echo "UPMO_RUN_KEY=upmo_rnk_$(openssl rand -base64 32)"
echo "UPMO_METRICS_TOKEN=upmo_mtr_$(openssl rand -base64 32)"
```

Put the database URLs together like this, with your own host, passwords, certificate files and database name:

```sh
UPMO_DATABASE_URL=postgres://upmo:<owner-password>@db.example.com:5432/upmo?sslmode=verify-full&sslrootcert=/etc/upmo/db-ca.pem&sslcert=/etc/upmo/upmo.crt&sslkey=/etc/upmo/upmo.key
UPMO_DATABASE_APP_URL=postgres://upmo_app:<app-password>@db.example.com:5432/upmo?sslmode=verify-full&sslrootcert=/etc/upmo/db-ca.pem&sslcert=/etc/upmo/upmo_app.crt&sslkey=/etc/upmo/upmo_app.key
UPMO_PUBLIC_URL=https://upmo.example.com
```

`UPMO_DATABASE_RUNNER_URL`, `UPMO_DATABASE_GATEWAY_URL` and `UPMO_DATABASE_SANDBOX_URL` follow the same pattern with their own role, password and client certificate. All URLs name the same database. The owner is a role that can run migrations; each other URL's user is always its role. If a password has special characters, percent-encode them in the URL.

In production every database URL must use `sslmode=verify-full` and name the database CA in `sslrootcert` and a client certificate in `sslcert` and `sslkey`.

Production refuses `sslmode=disable`, including a host that is one DNS label. Development still allows it for loopback and a single DNS label. The `sslmode=disable` URLs in `compose.yml` are the development stack.

## Ports

Ports on localhost in the `compose.yml` setup.

| Service | Port |
| --- | --- |
| `brain` | 8080 |
| `gateway` | 8082 |
| `sandboxd` | 8083 |

`upmo worker` listens on `UPMO_WORKER_LISTEN_ADDR` (`:8090`) inside the compose network only. It is not published on the host.

For key handling and encryption, see [Data at rest](operations/data-at-rest.md).
