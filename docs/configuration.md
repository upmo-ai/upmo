# Configuration

Copy `.env.example` to `.env` to override settings. `.env` is never committed.

## Environment

### Mandatory

The service does not start if one of these is missing.

| Variable | Needed by | What it does |
| --- | --- | --- |
| `UPMO_DATABASE_URL` | brain, gateway, `upmo` commands | Postgres URL. |
| `UPMO_DATABASE_APP_URL` | brain | Postgres URL for the `upmo_app` role. |
| `UPMO_PUBLIC_URL` | brain, gateway, `upmo` commands | Browser origin, an absolute http(s) URL. |
| `UPMO_MASTER_KEY` | brain, gateway, `upmo` commands that use secrets | Base64 of exactly 32 bytes. Seals credentials and default backup archives. Needed in every environment, including development. |
| `UPMO_GATEWAY_KEY` | gateway | `upmo_svc_gateway_` plus base64 of exactly 32 bytes. Gateway checks every call with it. Brain signs gateway calls with it and makes none without it. |
| `UPMO_SANDBOXD_KEY` | sandboxd | `upmo_svc_sandboxd_` plus base64 of exactly 32 bytes. Sandboxd checks every call with it. Brain needs it when `UPMO_SANDBOXD_URL` is set. |
| `UPMO_EDITOR_KEY` | sandboxd | `upmo_edk_` plus base64 of exactly 32 bytes. Brain signs editor links with it and sandboxd checks them. Without it, brain opens no editor. |
| `UPMO_MARKITDOWN_KEY` | markitdown sidecar | `upmo_svc_markitdown_` plus base64 of exactly 32 bytes. The sidecar checks every `/convert` call with it. Brain uses the sidecar only when this key and `UPMO_MARKITDOWN_URL` are set. |

Each process gets only the keys it signs or checks. A key for another service, or the old `upmo_svc_` master, stops the process at start. `UPMO_SERVICE_KEY` and `UPMO_SERVICE_KEY_PREVIOUS` are refused, even when empty.

### Optional

| Variable | Default | What it does |
| --- | --- | --- |
| `UPMO_ENV` | `production` | `production` or `development`. Production refuses the development keys and passwords `compose.yml` embeds. |
| `UPMO_LISTEN_ADDR` | `:8080` (`sandboxd`: `:8083`; `compose.yml` sets gateway to `:8082`) | Listen address. |
| `UPMO_PROXY_ADDR`, `UPMO_EDITOR_ADDR` | `:3128`, `:8084` | `sandboxd` egress proxy and editor addresses. |
| `UPMO_SANDBOX_DRIVER` | `auto` | `auto`, `docker`, `kubernetes` or `stub`. |
| `UPMO_METRICS_ADDR` | unset | `[host]:port` for a separate listener that serves Prometheus metrics at `GET /metrics`. When unset, no metrics listener runs. Must differ from `UPMO_LISTEN_ADDR` (and, for `sandboxd`, `UPMO_PROXY_ADDR` and `UPMO_EDITOR_ADDR`). Bind it to loopback or an internal network only. |
| `UPMO_METRICS_TOKEN` | unset | `upmo_mtr_` plus base64 of at least 32 bytes. Required when `UPMO_METRICS_ADDR` is set. Scrapers send it as `Authorization: Bearer <token>`. |
| `UPMO_RUN_KEY` | unset | `upmo_rnk_` plus base64 of exactly 32 bytes. Brain signs connector run tokens with it. |
| `UPMO_GATEWAY_KEY_PREVIOUS`, `UPMO_SANDBOXD_KEY_PREVIOUS`, `UPMO_MARKITDOWN_KEY_PREVIOUS`, `UPMO_EDITOR_KEY_PREVIOUS`, `UPMO_RUN_KEY_PREVIOUS` | unset | The old key during a rotation. It is still checked but never signs. |
| `UPMO_ARTIFACTS_DIR` | `/var/upmo/artifacts` | Absolute path of the artifacts volume. Brain creates it at start. Backups include it. |
| `UPMO_KNOWLEDGE_DIR` | `/var/upmo/knowledge` | Absolute path of the knowledge volume. Brain creates it at start. Backups include it. |
| `UPMO_MASTER_KEY_ID` | `k1` | ID of the current master key. |
| `UPMO_MASTER_KEY_PREVIOUS_<id>` | | Old master keys kept after a rotation, so older data and archives still open. |

This list is not complete. Other optional settings exist, such as `UPMO_TRUSTED_PROXIES`, `UPMO_WEBAUTHN_RP_ID` and `UPMO_SMTP_*`. If you set any `UPMO_SMTP_` value, `UPMO_SMTP_HOST` and `UPMO_SMTP_FROM` become required.

### Why 2 database URLs

Brain connects to the same Postgres database as 2 roles.

- `UPMO_DATABASE_URL` is the owner. Brain uses it to run migrations and to enable the app role. Gateway uses only this one. Sandboxd uses no database.
- `UPMO_DATABASE_APP_URL` is the role that serves web requests. It is granted only what the app needs. Tables use row-level security. A request cannot change the schema or read another team's rows.

The app URL must log in as `upmo_app` and must include a password. You do not create the role or set its password yourself. The migrations create `upmo_app`. Brain sets its password from this URL on every start.

To rotate the password, change it in `UPMO_DATABASE_APP_URL` and restart brain.

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

Put the two database URLs together like this, with your own host, passwords and database name:

```sh
UPMO_DATABASE_URL=postgres://upmo:<owner-password>@db.example.com:5432/upmo?sslmode=verify-full
UPMO_DATABASE_APP_URL=postgres://upmo_app:<app-password>@db.example.com:5432/upmo?sslmode=verify-full
UPMO_PUBLIC_URL=https://upmo.example.com
```

Both URLs name the same database. The owner is a role that can run migrations; the app URL user is always `upmo_app`. If a password has special characters, percent-encode them in the URL.

Unless the host is loopback or a single DNS label such as `postgres`, Upmo refuses a URL without `sslmode=verify-full`. The `sslmode=disable` URLs in `compose.yml` work only for the local stack.

## Ports

Ports on localhost in the `compose.yml` setup.

| Service | Port |
| --- | --- |
| `brain` | 8080 |
| `gateway` | 8082 |
| `sandboxd` | 8083 |

For key handling and encryption, see [Data at rest](operations/data-at-rest.md).
