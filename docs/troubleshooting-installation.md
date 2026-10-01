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

## 1. Container health failures

### `container docker-compose-server-1 is unhealthy`

**Symptom:** `install-cloud.sh` aborts with "Reticora server did not become
healthy".

**Causes and fixes:**

| Cause | Diagnosis | Fix |
|---|---|---|
| Session key unreadable | `docker compose logs server` shows `read session key: permission denied` or `is a directory` | Re-run the installer — `ensure_session_key` removes Docker-created directories and fixes ownership (`uid:65534`, mode `0640`). |
| Database password mismatch | `logs postgres` / `logs server` show `SQLSTATE 28P01` (password authentication failed) | Re-run the installer — `sync_db_password` resets the stored role password to the value in `.env` using a temporary trust-auth instance. **Manual alternative (data loss):** `docker compose down -v` removes the stale volume. |
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
resolves it, not the server. The installer derives the issuer from the public
base URL and warns when the two disagree.

**Fix:** Edit `.env`, set `RETICORA_OIDC_ISSUER_URL` to the publicly reachable
Keycloak URL (`https://<domain>/realms/reticora` behind the reverse proxy, or
`http://<host-ip>:8180/realms/reticora` for direct port publishing), then
re-run the installer.

### Login fails with "Invalid redirect uri"

**Cause:** The OIDC redirect URL is not whitelisted in the Keycloak client.

**Fix:** Re-run the installer — `configure_realm` substitutes the configured
`RETICORA_OIDC_REDIRECT_URL` (and its origin for CORS) into the realm file.

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
| `.env` | Existing values are defaults; secrets are regenerated only when absent. |
| Session key | Reused; a Docker-created directory or zero-byte file is removed and regenerated. |
| Database password | Probed; on `28P01` mismatch the stored role password is resynced automatically. |
| TLS bootstrap cert | Reused; regenerated when SAN-less or expiring within a day. |
| Let's Encrypt cert | Not re-issued when a real certificate is present. |
| Realm file | Re-rendered and validated on every run. |
| Migrations | Tracked in `reticora_schema_migrations`; already-applied versions are skipped. |

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
