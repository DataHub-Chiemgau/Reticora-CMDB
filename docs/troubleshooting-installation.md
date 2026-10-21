# Installation Troubleshooting

This guide collects the failure modes observed while hardening the installer
(PRs #11–#29) and explains how to diagnose and fix each of them. The
installers (`install.sh`, `install-cloud.sh`, `install-vm.sh`) are idempotent:
**re-running the installer is always the first recommended action** — many of
the issues below are detected and repaired automatically on a re-run.

All compose commands below are run from `deploy/docker-compose/`:

```bash
cd deploy/docker-compose
docker compose --env-file .env -f docker-compose.yml ps
```

---

## 0. Preflight refusals

The installers check the host before they change anything
(`install-preflight.sh`, INS-07).

| Message | Cause | Fix |
|---|---|---|
| `Platform … is not supported` | Not Ubuntu 22.04/24.04 LTS or Debian 12 (CH22) | Install on a supported system. `RETICORA_ALLOW_UNSUPPORTED_PLATFORM=1` continues, without support. |
| `Docker Engine … is too old` | Docker Engine below 24 | Upgrade Docker Engine (https://docs.docker.com/engine/install/), re-run. |
| `Docker Compose … is not supported` | The Python `docker-compose` v1 | Install the compose plugin (`apt-get install docker-compose-plugin`), re-run. |
| `… is required but could not be installed automatically` | No `apt-get` (not Debian/Ubuntu) | Install the named tool yourself, re-run. |
| `Only … available; the … minimum is …` (warning) | Host below the minimum sizing | The installation continues; see [ops/sizing.md](ops/sizing.md). |

---

## 1. Container health failures

### `container docker-compose-server-1 is unhealthy`

**Symptom:** `install-cloud.sh` aborts with "Reticora server did not become
healthy".

**Causes and fixes:**

| Cause | Diagnosis | Fix |
|---|---|---|
| Session key unreadable | `docker compose logs server` shows `read session key: permission denied` or `is a directory` | Re-run the installer — `ensure_session_key` removes Docker-created directories and fixes ownership (`uid:65534`, mode `0640`). |
| Database password mismatch | `logs postgres` / `logs server` show `SQLSTATE 28P01` (password authentication failed) | Re-run the installer — `sync_db_password` probes both databases (CMDB and Keycloak) over TCP and resets a stale role password to the value in `.env` using a temporary trust-auth instance. **Manual alternative (data loss):** `docker compose down -v` removes the stale volume. |
| Migrations not applied | `logs server` shows missing tables/columns | Re-run the installer; migrations run before the server starts. |

**General diagnosis:**

```bash
docker compose --env-file .env logs --tail 80 server
docker compose --env-file .env ps   # shows health status of every service
```

---

## 2. Keycloak / login failures

### `502 Bad Gateway` on the login screen (`/realms/...`)

**Symptom:** The web UI loads, but clicking "Sign in" ends in a 502 or an
error page from Keycloak.

**Cause:** The Keycloak container crash-loops because the bind-mounted realm
file `deploy/docker-compose/.generated/realm-reticora.json` is unreadable
inside the container (`java.nio.file.AccessDeniedException`) — the file must
be readable by uid 1000 / gid 0.

**Fix:** Re-run the installer. `configure_realm` re-renders the file with
container-readable permissions (640 with owner `1000:0`, or 644 as fallback)
and validates the JSON before Keycloak is started.

### Keycloak never becomes healthy

**Cause:** The `/health/ready` endpoint answers 404 because
`KC_HEALTH_ENABLED` was missing (fixed in the compose file).

**Diagnosis:**

```bash
docker compose --env-file .env logs keycloak | grep -i import
curl -fsS http://localhost:8180/health/ready
```

### Realm not imported / discovery document missing

**Symptom:** Keycloak is up, but
`http://<host>:8180/realms/reticora/.well-known/openid-configuration`
answers 404.

The installer now checks exactly this after startup
(`validate_keycloak_bootstrap`) and fails with a clear message instead of
letting the login fail later in the browser.

### Sign-in redirects to an unreachable `localhost` Keycloak

**Symptom:** After clicking "Sign in", the browser is sent to
`http://localhost:8180/...` on a remote machine.

**Cause:** `RETICORA_OIDC_ISSUER_URL` points at localhost — the browser
resolves it, not the server, and inside the backend container localhost is the
container itself. The installer therefore refuses a localhost issuer; for a
localhost base URL it proposes `http://<host-ip>:8180/realms/reticora`.

**Fix:** Edit `.env`, set `RETICORA_OIDC_ISSUER_URL` to the publicly reachable
Keycloak URL (`https://<domain>/realms/reticora` behind the reverse proxy, or
`http://<host-ip>:8180/realms/reticora` for direct port publishing), then
re-run the installer.

### OIDC issuer check failed

**Symptom:** The installer stops with exit code 1 after the server and the
frontend started (`✔ All services are up`) and before the summary, with one of

- `The OIDC issuer … is not reachable from this host`
- `The OIDC issuer … announces itself as '…'`
- `The backend container cannot reach the OIDC issuer …`

**Cause:** The issuer must be one and the same URL for the users' browsers,
this host and the backend container (INS-02): the browser fetches tokens from
it, the backend verifies them against the issuer they name. The installer
fetches `<issuer>/.well-known/openid-configuration` from the host and — via
`server -check-oidc-issuer` — from inside the backend container, and requires
the document to announce exactly the configured issuer.

**Order of the checks:**

1. After the Keycloak start: a localhost issuer is refused, and the realm must
   serve its discovery document on Keycloak's own port
   (`validate_keycloak_bootstrap`, see above). The initial administrator is
   created through the Keycloak admin API; neither step needs the configured
   issuer to answer.
2. Server and frontend start and must become healthy. Behind a domain the
   issuer `https://<domain>/realms/reticora` is served by the frontend's
   `/realms/` proxy, so it can only answer from now on.
3. The issuer from this host, then from inside the backend container. A
   failure stops the installer here. If this run has just created the initial
   administrator, its temporary password is printed with the error, because a
   re-run does not show it again.

**Fix:**

- *Not reachable from this host:* check DNS for the issuer's hostname, the
  firewall for the Keycloak port (default 8180) and the reverse proxy.
- *Announces another issuer:* Keycloak names itself after the address it is
  called under. Use the address it is published under (behind the reverse
  proxy `https://<domain>/realms/reticora`, else
  `http://<host-ip>:8180/realms/reticora`), not an alias.
- *Not reachable from the container:* the issuer's hostname must resolve to
  this host from inside Docker as well (no `/etc/hosts`-only entries, no
  split-horizon DNS without the containers), and a host firewall must allow
  the Docker bridge to reach the published port. With a private CA, set
  `RETICORA_OIDC_CA_CERT_FILE` to a CA bundle mounted into the container.

Run the container check manually:

```bash
cd deploy/docker-compose
docker compose --env-file .env run --rm --no-deps --entrypoint server server -check-oidc-issuer
```

Then correct `RETICORA_OIDC_ISSUER_URL` in `.env` and re-run the installer.

### Login fails with "Invalid redirect uri"

**Cause:** The OIDC redirect URL is not whitelisted in the Keycloak client.

**Fix:** Correct `RETICORA_OIDC_REDIRECT_URL` in `.env` if needed and re-run
the installer — `configure_realm` renders the redirect URL (and its origin for
CORS) into the realm file, and the installer applies the client's redirect
URIs and web origins to the running Keycloak (see section 6; Keycloak imports
the realm file only once).

### Keycloak admin login or rotation failed

**Symptom:** The installer stops after `Applying the Keycloak administrator
password and client settings …` with one of

- `Could not log in to the Keycloak admin API as <admin> with KEYCLOAK_ADMIN_PASSWORD or the previous password of …/.env.previous`
- `Could not set the rotated KEYCLOAK_ADMIN_PASSWORD …` / `Keycloak does not accept the rotated KEYCLOAK_ADMIN_PASSWORD …`
- `The Keycloak realm 'reticora' has no client …` / `Could not read the Keycloak client …` / `Could not apply <field> of the rendered realm …`

**Cause:** Keycloak takes `KEYCLOAK_ADMIN_PASSWORD` only when it creates the
master administrator at its first start. A changed password is applied by the
installer: it logs in with the password of `.env.previous` (the values of the
last run that reached Keycloak), sets the new one and logs in with it. The
login fails when Keycloak accepts neither password — typically because the
password was changed in the Keycloak console, or `.env.previous` was deleted
after `.env` had been edited.

**Fix:**

- *Login refused:* write the password Keycloak accepts today as
  `KEYCLOAK_ADMIN_PASSWORD=…` into `deploy/docker-compose/.env.previous`
  (mode `0600`) and re-run the installer; it then sets the value of `.env`.
- *Client missing or not writable:* the realm `reticora` must contain the
  client of the realm file (`reticora-app`). Check the Keycloak log
  (`docker compose --env-file .env logs keycloak`) and re-run the installer.

No password or secret is printed: the rotation lines (`Keycloak rotation: …`)
name the setting; only redirect URIs and web origins are shown with their
values.

---

## 3. TLS / certificate problems

### Browser shows "insecure" / Go OIDC client fails with `x509: certificate relies on legacy Common Name field`

**Cause:** A bootstrap certificate created by an older installer without a
subjectAltName.

**Fix:** Re-run the installer. `bootstrap_tls_cert` detects SAN-less or
expired bootstrap certificates and regenerates them with
`subjectAltName=DNS:<domain>`; `validate_tls_material` verifies the
certificate parses, carries the SAN, is unexpired and matches the private key.

### `x509: certificate signed by unknown authority` in server logs during login

**Cause:** While only the self-signed bootstrap certificate is installed
(Let's Encrypt not issued yet), the backend rejects the token exchange with
Keycloak.

**Fix:** Already handled: the installer sets `RETICORA_OIDC_CA_CERT_FILE` in
`.env` to the served fullchain, which the server trusts in addition to the
system store. Re-run the installer if the variable is missing.

### Let's Encrypt issuance fails

**Symptom:** Installer warns "Let's Encrypt certificate issuance failed".

**Causes:** DNS does not point at this host, or ports 80/443 are not reachable
from the internet (the ACME http-01 challenge needs port 80).

**Fix:** Fix DNS/firewall, then re-run the installer — issuance is retried and
nginx is reloaded automatically. The stack keeps working with the bootstrap
certificate in the meantime. Logs:
`docker compose --env-file .env logs certbot`.

---

## 4. Frontend container problems

### Frontend crash-loops with an nginx `[emerg]` config error

**Cause:** Invalid `pid` path or an unwritable temp directory for the
unprivileged `nginx` user (PR #13/#17). The current image places all runtime
paths under `/tmp`, which is backed by a writable tmpfs in the compose file.

**Diagnosis:** `docker compose --env-file .env logs frontend`

### Blank dashboard after login

**Cause (fixed in PR #29):** empty list endpoints serialized `null` instead of
`[]` and the UI crashed on `.filter`. The backend now always serializes empty
lists as `[]`; the UI defensively guards against null data.

---

## 5. Web Crypto / insecure origin

### Login fails with "Cannot read properties of undefined (reading 'digest')"

**Cause:** The PKCE challenge requires the Web Crypto API, which browsers only
expose in *secure contexts* (HTTPS or `localhost`). Accessing the UI via a
plain-HTTP LAN IP (e.g. `http://192.168.x.x:3000`) breaks this.

**Fix:** Access the UI via `localhost`, or enable HTTPS (enter a public domain
in the installer). The UI now shows a readable error instead of crashing.

---

## 6. Idempotency: safe re-runs

The installer is safe to re-run and reuses existing `.env` values as defaults:

| Resource | Behaviour on re-run |
|---|---|
| `.env` | Existing values are defaults; secrets are regenerated only when absent. Settings the installer does not write itself (e.g. `RETICORA_HTTP_BIND`) are kept in a section at the end. |
| `.env.previous` | The `.env` of the last run that reached Keycloak (mode `0600`, git-ignored): created from the existing `.env` before the merge when missing, kept until the Keycloak values of the new `.env` are applied, then replaced by it. It holds the previous admin password of a rotation. |
| Session key | Reused; a Docker-created directory or zero-byte file is removed and regenerated. |
| Database passwords | `RETICORA_DB_PASSWORD` (CMDB), `RETICORA_DB_APP_PASSWORD` (runtime login) and `KEYCLOAK_DB_PASSWORD` (Keycloak) are probed over TCP — the images trust the local socket and 127.0.0.1 regardless of the password — and on mismatch the stored role password is resynced automatically. |
| Keycloak admin password | A changed `KEYCLOAK_ADMIN_PASSWORD` is set through the admin CLI: login with the password of `.env.previous`, then with the new one; afterwards the old password no longer works. |
| OIDC client `reticora-app` | Secret (`RETICORA_OIDC_CLIENT_SECRET`), redirect URIs and web origins (from `RETICORA_OIDC_REDIRECT_URL` and `RETICORA_OIDC_ISSUER_URL`) of the rendered realm file are compared with the running realm; each differing value is applied with one `kcadm update clients/<id>`. |
| TLS bootstrap cert | Reused; regenerated when SAN-less or expiring within a day (a running frontend reloads it before the issuer checks). |
| Let's Encrypt cert | Not re-issued when a real certificate is present. |
| Realm file | Re-rendered and validated on every run. Keycloak imports it only while the realm does not exist (`start --import-realm` skips an existing realm). |
| Migrations | Tracked in `reticora_schema_migrations`; already-applied versions are skipped. |

**Rotating passwords and OIDC settings:** change the value in `.env` (or at
the installer's prompt) and re-run the installer. Every applied Keycloak
change is logged once as `Keycloak rotation: …`, passwords and secrets without
their value; a run without a change writes nothing to Keycloak.

**Limits of the rotation:**

- Of the realm file only the client's secret, redirect URIs and web origins
  reach an existing realm; these three follow the realm file, so a change made
  to them in the Keycloak console is reset by the next run. Other realm
  content — roles, groups, flows, password and MFA policies, mappers, the user
  profile — is not updated by a re-run; change it in the Keycloak console.
- The name of the Keycloak administrator (`KEYCLOAK_ADMIN`) is not renamed;
  only its password is rotated.
- `RETICORA_MASTER_KEY` and the session signing key are not rotated by the
  installer (see [ops/workers.md](ops/workers.md), "Key rotation"). Do not
  change `RETICORA_MASTER_KEY` in `.env`: the server could no longer decrypt
  the stored credentials.

If a stale volume must be discarded completely (**all CMDB data is lost**):

```bash
cd deploy/docker-compose
docker compose --env-file .env down -v
./install-cloud.sh   # from the repository root
```

---

## 7. Collector offline spool

While the backend is unreachable the collector buffers discovery results in
`RETICORA_SPOOL_DIR` and delivers them, oldest source time first, before any
new results once the connection is back (COL-05).

- **No loss within 24 h:** unacknowledged batches younger than 24 hours are
  never dropped, whatever `RETICORA_SPOOL_MAX_AGE` says (NFR-04). Older batches
  are dropped after `RETICORA_SPOOL_MAX_AGE` (default 72 h).
- **Backpressure:** at 90 % of `RETICORA_SPOOL_MAX_BYTES` (default 1 GiB) the
  collector pauses discovery (`"event":"collector.spool.backpressure"` in the
  collector log) instead of overwriting data. Discovery resumes as soon as the
  spool drains.
- **Loss reporting:** every unavoidable loss (age limit beyond 24 h, size limit,
  full spool) is logged as `"event":"collector.spool.data_lost"` with reason and
  size, counted, and reported with the next heartbeat. The server logs
  `"event":"collector.spool.degraded"` with organization, collector and the
  dropped counters.

If the collector reports backpressure: check connectivity to `RETICORA_SERVER_URL`,
free disk space for the spool, or raise `RETICORA_SPOOL_MAX_BYTES`.

---

## 8. Verifying a healthy installation

```bash
# All containers healthy
docker compose --env-file .env ps

# Backend
curl -fsS http://localhost:8080/healthz

# Keycloak realm
curl -fsS http://localhost:8180/realms/reticora/.well-known/openid-configuration | grep issuer

# Frontend
curl -fsS http://localhost:3000/ | head
```

The CI workflow `.github/workflows/install-smoke.yml` runs these exact checks
on every change to the installer or the compose stack.
