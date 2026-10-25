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
| DDL event trigger disabled | `logs server` shows `database is not ready … event trigger reticora_restrict_app_ddl is not enabled` (or `is missing`); `/readyz` answers `"database":"unavailable"` | An aborted TimescaleDB update left it disabled — see [TimescaleDB extension update failed](#timescaledb-extension-update-failed). Re-run the installer; the update step enables it again. |

**General diagnosis:**

```bash
docker compose --env-file .env logs --tail 80 server
docker compose --env-file .env ps   # shows health status of every service
```

### TimescaleDB extension update failed

**Symptom:** `install-cloud.sh` aborts with "The TimescaleDB extension update
failed", after a line such as
`timescaledb-update: error: reticora: ALTER EXTENSION timescaledb UPDATE failed; timescaledb stays at 2.17.2`.
Updating by hand without the script ends with

```text
FATAL:  "timescaledb" already loaded with a different version
DETAIL:  The new version is "2.31.0", this session is using version "2.17.2". The session will be restarted.
```

**Cause:** a database keeps the TimescaleDB extension version it was created
with when the database image brings a newer TimescaleDB. Before the
migrations, the installer stops the server and runs
`scripts/ops/timescaledb-update.sh`, which raises the extension in `reticora`,
`postgres` and `template1` to the image's version (WP-604). The update must be
the first statement of its session; in `reticora` the event trigger
`reticora_restrict_app_ddl` would load the old library first (the FATAL
above), so the script disables it only around the update and enables it
again, also when the update fails.

**Fix:**

- Read the `timescaledb-update:` lines and the PostgreSQL error before them;
  `docker compose --env-file .env logs --tail 80 postgres` shows the server
  side. Re-run the installer; the step is idempotent and leaves a current
  extension alone.
- `the application is still connected (<logins>)`: a login of the
  application roles still had a session after 15 seconds
  (`TSDB_UPDATE_SESSION_WAIT`); the script does not disable the trigger while
  the application can run DDL. Stop every process that uses the runtime login
  (`docker compose --env-file .env stop server`; Kubernetes: scale
  `reticora-server` to 0) and re-run.
- `timescaledb … is newer than the installed library`: the database was
  served by a newer image. Install a release whose `reticora-postgres` image
  carries at least that TimescaleDB version; this image cannot serve it.
- `the event trigger reticora_restrict_app_ddl could not be enabled again`:
  enable it by hand before the server starts, then check the state (`O` is
  enabled):

  ```bash
  docker compose --env-file .env exec -T postgres psql -X -U reticora -d reticora \
    -c 'ALTER EVENT TRIGGER reticora_restrict_app_ddl ENABLE'
  docker compose --env-file .env exec -T postgres psql -X -U reticora -d reticora \
    -Atc "SELECT evtenabled FROM pg_event_trigger WHERE evtname = 'reticora_restrict_app_ddl'"
  ```

The server refuses to start, and `/readyz` reports `"database":"unavailable"`,
while the trigger is disabled or missing; an extension below the version of
the installed library is logged by the server as a warning at start
(`PostgreSQL extension is not at the version of its installed library`). On
Kubernetes with an external database the operator runs the script before the
rollout (`deploy/README.md`, section 3).

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

### How the Let's Encrypt certificate is issued

With a domain (`RETICORA_TLS_DOMAIN`) the installer first creates a
self-signed placeholder in `letsencrypt/live/<domain>/` so that nginx can
start, then `issue_tls_certificate` requests the real certificate:

1. **Placeholder backup.** certbot refuses to store a new certificate while
   `live/<domain>/` exists ("CertStorageError: live directory exists", after
   the ACME order has been placed), and an existing
   `renewal/<domain>.conf` — e.g. the empty one such a failed request leaves
   behind — makes it create a second lineage `live/<domain>-0001/`. The
   installer therefore moves `live/<domain>`, `archive/<domain>` and
   `renewal/<domain>.conf` into `letsencrypt/reticora-placeholder/<domain>/`
   before the request (files certbot created belong to root; without root
   the installer uses `sudo -n`, otherwise it stops and names the paths).
2. **Request.** `certbot certonly --webroot … -d <domain> --cert-name
   <domain> --deploy-hook "sh /usr/local/bin/reticora-certbot-deploy-hook.sh"`
   in a one-off `certbot` container stores the lineage in
   `live/<domain>/` (symlinks into `archive/<domain>/`).
3. **Failure.** When the request fails (challenge, rate limit, storage
   error), whatever certbot left of the lineage is removed and the
   placeholder moves back: the stack keeps serving it, nothing is left that
   would force a `-0001` lineage on the next run. If certbot reports an
   error only after it stored a verifying certificate, the certificate is
   kept and checked as below.
4. **Success.** The backup is deleted, the chain is checked against the trust
   store (`validate_tls_material <domain> 1`), then `nginx -t` runs in the
   frontend (as its nginx user, uid 101) and only after that
   `nginx -s reload`. A re-run with a valid certificate places no order.

**File permissions (deploy hook).** certbot runs as root and keeps
`archive/` and `live/` at `0700` and the private key at `0600`; nginx in the
frontend runs as uid/gid 101 and reads the files through the read-only bind
mount `./letsencrypt:/etc/letsencrypt`. `deploy/docker-compose/certbot-deploy-hook.sh`
runs after the issuance (installer) and after every renewal (`certbot renew`
loop of the `certbot` service) and sets:

| Path | Owner | Mode |
|---|---|---|
| `archive/`, `live/`, `archive/<domain>/`, `live/<domain>/` | root | `0755` |
| `cert*.pem`, `chain*.pem`, `fullchain*.pem` | root | `0644` |
| current `privkey*.pem` | root, group **101** | `0640` |

Of the two options (group 101, or a copy owned by uid 101 in a directory of
its own) the hook uses the group: nginx keeps reading the paths certbot
manages (`live/<domain>/`), certbot carries the group and mode of the key
over to each renewed key, and no second copy of the key exists. The key is
never world-readable. The directories stay `0755` instead of `0750`: the
chain is public, and the server container (user nobody) reads
`live/<domain>/fullchain.pem` as `RETICORA_OIDC_CA_CERT_FILE` and does not
start without it; an installer running without root reads it as well. Such
an installer cannot read the key itself; `nginx -t` checks the pair instead.

**Effect on the host:** group 101 is a number, so it applies to the host's
`deploy/docker-compose/letsencrypt/` as well. Members of the host group with
gid 101 (`getent group 101`; on Debian/Ubuntu often `ssl-cert`,
`systemd-journal` or a service group) can read the private key there.
Remove such members or keep the directory out of their reach (e.g. a
`0700` parent directory) if they must not.

### Let's Encrypt issuance fails

**Symptom:** Installer warns "Let's Encrypt certificate issuance failed".

**Causes:** DNS does not point at this host, or ports 80/443 are not reachable
from the internet (the ACME http-01 challenge needs port 80).

**Fix:** Fix DNS/firewall, then re-run the installer — issuance is retried and
nginx is reloaded automatically. The stack keeps working with the bootstrap
certificate in the meantime (the placeholder is restored after the failed
request). Logs:
`docker compose --env-file .env logs certbot`.

An installation that ran into the former "live directory exists" error may
still hold a second lineage `live/<domain>-0001/`; the installer no longer
uses it. Remove it with
`docker compose --env-file .env run --rm --no-deps --entrypoint certbot certbot delete --cert-name <domain>-0001`.

### Issued certificate not loadable by nginx

**Symptom:** After "Requesting a Let's Encrypt certificate" the installer
aborts with rc 1: "nginx in the frontend (uid 101) cannot load the
certificate and key certbot stored in …/letsencrypt/live/<domain> (nginx -t
failed)", preceded by the output of `nginx -t` (e.g. `cannot load
certificate key … Permission denied`). It does not report "Let's Encrypt
certificate issued" or "HTTPS is now active". nginx still serves the
placeholder from memory, and the frontend does not start again until the
files are readable.

**Cause:** The deploy hook did not run or failed (certbot logs an error of
the deploy hook in its output), e.g. because
`deploy/docker-compose/certbot-deploy-hook.sh` is missing from the compose
directory, so the key is still `0600` root in an `archive/` with `0700`.

**Fix:** Restore `certbot-deploy-hook.sh` from the release/checkout if it is
missing (Docker creates an empty directory of that name for a missing
bind-mount source; remove it first). Run the hook for the lineage, then
check and reload nginx:

```bash
docker compose --env-file .env run --rm --no-deps --entrypoint sh \
  -e RENEWED_LINEAGE=/etc/letsencrypt/live/<domain> certbot \
  /usr/local/bin/reticora-certbot-deploy-hook.sh
docker compose --env-file .env exec -T frontend nginx -t
docker compose --env-file .env exec -T frontend nginx -s reload
```

A re-run of the installer does not request the certificate again (it is
valid); it relies on the permissions being fixed.

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
| Let's Encrypt cert | Not re-issued when a real certificate is present (no ACME order, no `-0001` lineage). A failed request restores the placeholder. |
| Realm file | Re-rendered and validated on every run. Keycloak imports it only while the realm does not exist (`start --import-realm` skips an existing realm). |
| TimescaleDB extension | The server is stopped before the migrations; an extension below the version of the current database image is updated (`scripts/ops/timescaledb-update.sh`), a current one is left alone. |
| Migrations | Tracked in `reticora_schema_migrations`; already-applied versions are skipped. |
| Image pins (release mode) | `RETICORA_SERVER_IMAGE`, `RETICORA_FRONTEND_IMAGE` and `RETICORA_POSTGRES_IMAGE` are written by the installer: verified again with cosign and re-pinned on every run of the release mode (the default), empty for the local source build (`--build-from-source`). A pin of an earlier run is never reused unverified. The mode is not remembered: every run without `--build-from-source` installs the signed release (section 9). |

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

---

## 9. Signature verification

The release workflow (`.github/workflows/release.yml`) signs its images,
binaries and the manifest `SHA256SUMS` keyless with cosign (TEC-15, E-31).
The installers verify these signatures **before** they install anything;
every check is fail-closed: a failed or impossible check stops the
installation with rc 1 and changes nothing that is running.

**Default and switch (E-69, Option 1):** by default every installer installs
the **signed release artifacts** of one release — the one you name
(`install.sh --ref v<version>`, `install-cloud.sh`/`install-vm.sh --release
v<version>`, `RETICORA_RELEASE`), otherwise the newest release (GitHub's
latest release, no pre-release). Only `--build-from-source`
(`RETICORA_BUILD_FROM_SOURCE=1`) builds locally instead; that installation is
**not signature-verified**, the installer says so at the start. A ref that is
no release tag (a branch such as `main`, a commit) is accepted only with the
switch.

| Installer path | Verification |
|---|---|
| `install.sh` (bootstrap, e.g. piped from curl) | Downloads the source of the release tag (default: the newest release), checks the signed `SHA256SUMS`, then the commit of the checked-out tag against `SOURCE_COMMIT` (git) or the sha256 of `reticora-source-<version>.tar.gz` (without git, checked before it is unpacked), and hands the release to the component installer (`RETICORA_RELEASE`). A mismatch stops before anything of the source runs. `--ref main` without the switch stops before the download. |
| `install-cloud.sh` (default: release mode) | The release: `--release`, otherwise the release tag the checkout is on, otherwise the newest release. First the checkout itself — compose file, realm and migrations come from it — must be the signed source of that release: a git checkout its commit (`SOURCE_COMMIT`), a directory without git every file of the signed source tarball. Then `cosign verify` of `reticora-server`, `reticora-frontend` and `reticora-postgres` (`<registry>/<image>:<release>`, registry `ghcr.io/datahub-chiemgau` or `RETICORA_IMAGE_REGISTRY`). The verified digest is pinned in `.env` (`RETICORA_*_IMAGE=<registry>/<image>@sha256:…`), only these images are pulled, nothing is built — also not PostgreSQL. All before `compose up`. |
| `install-vm.sh` (default, systemd mode) | The collector binary `reticora-collector-linux-<arch>` of the release (`--release`, `RETICORA_COLLECTOR_RELEASE`, otherwise the newest): `cosign verify-blob` with its `.sigstore.json`, and its sha256 against the signed `SHA256SUMS`, before it is copied to `/opt/reticora-collector`. No sources, no local binary. |
| `install-vm.sh --docker` (default) | `cosign verify` of the image `reticora-collector` of the release; the generated compose project runs it pinned by digest (`image: <registry>/reticora-collector@sha256:…`), pulled, not built. |
| `--build-from-source` (all three) | **Not signature-verified:** `install-cloud.sh` builds server, frontend and PostgreSQL from the checkout, `install-vm.sh` builds the collector (binary or image). Still checked: a release tag as source (`--ref v<version>`, `RETICORA_REPO_REF=v<version>`) against the signed release, and the Go toolchain `install-vm.sh` downloads against the sha256 that go.dev publishes (`https://go.dev/dl/?mode=json&include=all`). Another ref prints `Source not signature-verified (no release tag)`. `--release` and `--build-from-source` exclude each other. |

The signer must be `release.yml` of `DataHub-Chiemgau/Reticora-CMDB` on a
version tag, issued by `https://token.actions.githubusercontent.com` — the
identity the collector updater pins as well. The signature must be from
exactly the selected release, so the signed files of an older release do not
pass as the requested one. Only the tag of the newest release is taken from
the GitHub API (`https://api.github.com/repos/DataHub-Chiemgau/Reticora-CMDB/releases/latest`);
everything installed for it is verified for that tag. Releases are always
fetched from
`https://github.com/DataHub-Chiemgau/Reticora-CMDB/releases/download/<tag>/`;
a git mirror (`--repo`) is accepted when its tag is the signed commit, an
image mirror (`RETICORA_IMAGE_REGISTRY`) when it keeps the signatures
(`cosign copy`).

**cosign:** an installed `cosign` (v2) is used. Otherwise the installer
downloads cosign v2.4.1 for linux/amd64 or linux/arm64 from the sigstore
release and checks it against its published sha256 before every use (cache:
`RETICORA_TOOLS_DIR`, default `/var/cache/reticora` as root, otherwise
`~/.cache/reticora`).

**Typical calls:**

```bash
# Signed release, the newest or a named one (the default)
curl -fsSL https://raw.githubusercontent.com/DataHub-Chiemgau/Reticora-CMDB/main/install.sh | bash -s -- --cloud
./install.sh --cloud --ref v1.2.3            # source, images: release v1.2.3
sudo ./install.sh --vm --ref v1.2.3          # collector binary of v1.2.3
git checkout v1.2.3 && ./install-cloud.sh    # the release of the checkout's tag
# Local build of a checkout (development, CI) — not signature-verified
./install.sh --cloud --build-from-source
RETICORA_BUILD_FROM_SOURCE=1 ./install-cloud.sh --non-interactive
sudo ./install-vm.sh --build-from-source
```

Re-runs follow the same rule: an installation made with `--build-from-source`
switches to the signed release images on a re-run without the switch (the
checkout must then be the source of that release); pass the switch again to
keep building locally.

**Check by hand** (the commands the installers run):

```bash
identity='^https://github\.com/DataHub-Chiemgau/Reticora-CMDB/\.github/workflows/release\.yml@refs/tags/v1\.2\.3$'
issuer=https://token.actions.githubusercontent.com
# The newest release
curl -fsSL https://api.github.com/repos/DataHub-Chiemgau/Reticora-CMDB/releases/latest | jq -r .tag_name
# Images: prints the verified digest (critical.image.docker-manifest-digest)
cosign verify ghcr.io/datahub-chiemgau/reticora-server:v1.2.3 \
  --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer"
# Release files: manifest first, then each file against it
base=https://github.com/DataHub-Chiemgau/Reticora-CMDB/releases/download/v1.2.3
curl -fsSLO "$base/SHA256SUMS" -fsSLO "$base/SHA256SUMS.sigstore.json" \
  -fsSLO "$base/reticora-collector-linux-amd64" -fsSLO "$base/reticora-collector-linux-amd64.sigstore.json"
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer" SHA256SUMS
cosign verify-blob --bundle reticora-collector-linux-amd64.sigstore.json \
  --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer" reticora-collector-linux-amd64
sha256sum -c --ignore-missing SHA256SUMS
# Source: the commit of the tag
curl -fsSL "$base/SOURCE_COMMIT"; git -C /opt/reticora-cmdb rev-parse HEAD
```

**Error messages:**

| Message | Cause | Fix |
|---|---|---|
| `Source not signature-verified (no release tag): '<ref>' is not installed by default …` | `--ref` (or `RETICORA_REPO_REF`) names a branch or commit, not a release tag | Install a release (`--ref v<version>`, or no `--ref` for the newest), or add `--build-from-source` to build that ref locally, unverified. |
| `Could not determine the newest release of …` | No `--ref`/`--release` and the GitHub API was not reachable (network, rate limit) or names no version tag | Name the release (`install.sh --ref v<version>`, `--release v<version>`), or use `--build-from-source`. |
| `Source check failed: the checkout of <tag> in <dir> is commit …, the signed release names …` | The tag in the repository (or a mirror) points to another commit than the release, an old checkout could not be updated, or `install-cloud.sh` runs in a checkout that is not the release (e.g. `main`, or a tag other than the selected release). Commit `<none>`: git refused to read the checkout (e.g. "dubious ownership" when it belongs to another user than the one running the installer) | Do not build. Check out the release tag (`git checkout v<version>`), install with `./install.sh --cloud --ref v<version>` (downloads the source), name the checkout's release with `--release`, or use `--build-from-source` for a development checkout. |
| `Source check failed: <dir> is not the source of release <tag> (<file>: FAILED …)` | `install-cloud.sh` runs in a directory without git whose files differ from the signed source tarball of the release (changed, missing, or another version) | As above; `./install.sh --cloud --ref v<version>` restores the release source. |
| `Signature check failed: <file or image> is not signed by the release workflow …` | cosign rejected the signature: manipulated file or image, a signature of another identity (fork, other workflow) or of another release, a tag without signed images, or no registry access | Do not install. Check the tag and the registry (`docker login` for a private registry) and verify by hand as above; a mirror must keep the signatures (`cosign copy`). |
| `Signature check failed: the verified signatures of <ref> do not name <image> with one digest` | The tag carries a signed image of another name (e.g. the frontend image under `reticora-server`) | Do not install; the registry content is wrong. |
| `Checksum mismatch: <file> … is not the file of the signed manifest SHA256SUMS` | The downloaded binary, tarball or `SOURCE_COMMIT` is not the one of the release | Do not install; download again from the release page. |
| `cosign is required to verify the signed release artifacts …` | No cosign installed and the download from github.com/sigstore failed (or no build for this CPU) | Install cosign v2 (https://docs.sigstore.dev/cosign/system_config/installation/) and re-run. |
| `The downloaded cosign v2.4.1 does not match its published sha256 checksum` | The cosign download was manipulated or truncated | Re-run; install cosign yourself if it persists. |
| `Could not download <file> of release <tag> …` | The release has no such file (tag not released, or released before WP-387 without `SHA256SUMS.sigstore.json`) or no network | Use a release that publishes the signed manifest, or `--build-from-source`. |
| `… --build-from-source …; use one of them` | `--release` together with `--build-from-source` | Choose the signed release or the local build. |
| `No release selected for the signed collector …` / `No registry or release selected for the signed images …` | A function of the installer ran without a selected release (internal call) | Name the release with `--release v<version>`, or use `--build-from-source`. |
| `RETICORA_BUILD_FROM_SOURCE must be 1 (build from source) or 0 …` | Another value in the variable | Set `RETICORA_BUILD_FROM_SOURCE=1` or leave it unset. |
| `Checksum mismatch: the downloaded go<version>.linux-<arch>.tar.gz is not the file go.dev publishes` | The Go toolchain download of the source build was manipulated | Re-run; install Go yourself if it persists. |
| `… the Go toolchain is not installed unverified` (warning) | go.dev's checksum list was not reachable (source build) | The installer falls back to a prebuilt `bin/collector` or the existing binary; otherwise install Go yourself. |
| `… not signature-verified (--build-from-source)` (warning) | The local build was requested | Expected for development and CI; production installs the signed release (no switch). |
