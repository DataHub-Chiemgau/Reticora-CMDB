# Reticora CMDB — Installation

Reticora ships with interactive ("foolproof") installers for both halves of the
hybrid architecture. They prompt for **all** important information, validate
the input, generate required secrets and can be re-run safely (idempotent).

| Component | Installer | Target |
|---|---|---|
| Central cloud | `./install-cloud.sh` | Docker host running the full stack (PostgreSQL/TimescaleDB, NATS, Redis, MinIO, Keycloak, API server, web UI) |
| VM in the customer network | `./install-vm.sh` | Collector VM (systemd service or Docker container) that scans the local network and uploads via mTLS |

`./install.sh` is a convenience entry point that asks which of the two
components should be installed and delegates accordingly.

## 0. One-line bootstrap (no checkout required)

The entry point is self-bootstrapping: when the sources are missing it
installs the tools needed for the download (curl/git/tar), downloads the
repository and re-executes the matching installer from there. A bare VM only
needs:

```bash
curl -fsSL https://raw.githubusercontent.com/DataHub-Chiemgau/Reticora-CMDB/main/install.sh | bash -s -- --cloud
```

**Signed release by default (TEC-15, E-69 Option 1).** The installers install
the signed release artifacts of the newest release, or of the one named with
`--ref v<version>`: the source of the release tag (checked against the signed
`SOURCE_COMMIT` or source tarball), the images `reticora-server`,
`reticora-frontend` and `reticora-postgres` (`cosign verify`, pinned by
digest) and the collector (`cosign verify-blob` of the binary, or the signed
image in the Docker mode). `--build-from-source` (`RETICORA_BUILD_FROM_SOURCE=1`)
builds everything locally from the sources instead — **not
signature-verified**, the installer warns at the start; only then is a ref
that is no release tag (e.g. `main`) accepted. Without the switch such a ref
stops with a hint. Every check, its error messages and the commands to verify
by hand: [troubleshooting-installation.md, section 9](../docs/troubleshooting-installation.md#9-signature-verification).

Options for the bootstrap:

| Option | Environment variable | Default |
|---|---|---|
| `--repo <url>` | `RETICORA_REPO_URL` | public repository |
| `--ref <ref>` | `RETICORA_REPO_REF` | the newest release (with `--build-from-source`: `main`) |
| `--build-from-source` | `RETICORA_BUILD_FROM_SOURCE=1` | off: the signed release artifacts are installed |
| `--source-dir <dir>` | `RETICORA_SOURCE_DIR` | `/opt/reticora-cmdb` (root) / `~/reticora-cmdb` |

From there on the component installers handle everything else themselves:
missing dependencies are installed (Docker Engine, Docker Compose, curl,
openssl, git, tar and — for the collector's source build — the Go toolchain),
the configuration is generated and the services are started and verified.

A development checkout is installed with the switch:
`./install.sh --cloud --build-from-source` (CI does the same, see
`.github/workflows/install-smoke.yml`).

## 1. Central cloud

```bash
./install-cloud.sh
```

The installer asks for:

- **Passwords/secrets** — database, MinIO/S3, Keycloak admin, Keycloak DB,
  OIDC client secret, master key for credential encryption. Secure random
  defaults are generated automatically and reused on re-runs. The password of
  the server's runtime database login (`RETICORA_DB_APP_PASSWORD`) is always
  generated and never asked for.
- **Public URLs** — web UI base URL, OIDC issuer, OIDC redirect URL. The
  **OIDC issuer URL is handed to the browser** as the Keycloak sign-in
  endpoint, so it must be reachable from your users' machines. Do **not**
  leave it at `http://localhost:8180/...` unless the browser runs on the
  server itself — otherwise sign-in fails with a "cannot reach localhost"
  error. The installer derives a matching default from the public base URL.
  For a public hostname that default is `<public base URL>/realms/reticora`:
  the web UI's nginx proxies `/realms/` and `/resources/` to Keycloak, so the
  login screen is served from the same address as the UI (the admin console
  stays on the Keycloak host port only).
- **HTTPS / Let's Encrypt (optional)** — a public domain name (e.g.
  `cmdb.example.com`) and an e-mail address for certificate expiry notices.
  When a domain is entered, the installer automatically obtains a free Let's
  Encrypt certificate (nginx + Certbot, ACME http-01 challenge) and enables
  HTTPS with automatic renewal. Leave empty to keep plain HTTP.
- **Ports** — published host ports for UI (default 3000; fixed to 80 when
  HTTPS is enabled — port 80 answers the ACME challenge and redirects to
  HTTPS, the UI itself is then served at `https://<domain>` on port 443),
  API (8080) and Keycloak (8180).
- **Image registry** — the registry of the signed release images (default
  `ghcr.io/datahub-chiemgau`, or `RETICORA_IMAGE_REGISTRY` for a mirror that
  keeps the signatures). The release is `--release v<version>`, otherwise the
  release tag of the checkout, otherwise the newest release; the checkout
  (compose file, realm, migrations) must be the signed source of that release.
  `./install-cloud.sh --build-from-source` builds the images from the checkout
  instead (not signature-verified).

It then:

1. checks the prerequisites (Docker + Compose, openssl, curl) and installs
   whatever is missing automatically (Docker Engine via get.docker.com, the
   Compose plugin and the remaining tools via the distribution's package
   manager) — it also starts/enables the Docker daemon when it is not running,
   and, by default, checks that the checkout is the signed source of the
   selected release (its git commit against `SOURCE_COMMIT`, otherwise every
   file against the signed source tarball),
2. writes `deploy/docker-compose/.env` (mode `600`),
3. generates the RS256 session signing key at
   `deploy/docker-compose/secrets/session-private.pem` (group-readable by the
   container's `nobody` group so the server process can read it; the file is
   bind-mounted into the server container),
4. renders the Keycloak realm with the configured OIDC client secret into
   `deploy/docker-compose/.generated/`,
5. verifies the signed release images with cosign, pins them by digest in
   `.env` and pulls them (default), or builds server, frontend and PostgreSQL
   from the checkout (`--build-from-source`, not signature-verified),
6. starts the infrastructure and applies the SQL migrations from
   `backend/migrations/` (via `golang-migrate` when installed, otherwise via
   `psql` inside the postgres container; applied versions are tracked in the
   `reticora_schema_migrations` table so re-runs are no-ops), then creates
   or repairs the runtime login `reticora_runtime` (see below),
7. starts Keycloak, removes the demo administrator of earlier releases and
   creates the individual initial administrator (`RETICORA_INITIAL_ADMIN_EMAIL`)
   with a random temporary password, then starts the API server and the
   frontend,
8. when a TLS domain is configured: requests the Let's Encrypt certificate
   with Certbot (webroot challenge served by nginx) and reloads nginx,
9. verifies `/healthz` on the API,
10. optionally garbage-collects superseded container images (keeps the newest
    `RETICORA_GC_KEEP_IMAGES`, default 3, per image).

Afterwards the web UI is reachable at the configured base URL. The installer
prints the initial administrator's temporary password **once**; the first
login requires a new password and the setup of a one-time-password app
(TOTP). Re-runs keep the existing administrator and do not show the password
again.

Non-interactive (CI) usage: `./install-cloud.sh --non-interactive` uses the
existing `.env` values, generated defaults and never prompts. CI installs the
checked-out, unpublished state with `--build-from-source`; the default path
runs on release tags (job `release-mode` of `install-smoke.yml`).

### Database logins (TEN-03)

The stack uses two PostgreSQL logins:

| Login | Password (`.env`) | Used by | Rights |
|---|---|---|---|
| `reticora` | `RETICORA_DB_PASSWORD` | migrations, seed, `backup` service | bootstrap superuser of the postgres image |
| `reticora_runtime` | `RETICORA_DB_APP_PASSWORD` | the server (`RETICORA_DATABASE_URL`) | `LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`, member of `reticora_app` only |

The server switches every pooled connection into `reticora_app` (`SET ROLE`);
because the login itself has no SUPERUSER or BYPASSRLS, a `RESET ROLE` does not
lift row level security. After the migrations the installer creates
`reticora_runtime`, or resets an existing one to exactly these attributes and
memberships, and sets its password from `.env`. Re-running the installer on an
installation from before this login switches the server over without touching
any data; changing `RETICORA_DB_APP_PASSWORD` in `.env` and re-running rotates
the password.

### HTTPS with Let's Encrypt (nginx + Certbot)

To serve the web UI over HTTPS with a free, auto-renewing Let's Encrypt
certificate, enter a **public domain name** when the installer asks for it
(e.g. `cmdb.example.com`). Prerequisites:

- a DNS A/AAAA record for the domain pointing at this host,
- ports **80** and **443** reachable from the internet (the ACME http-01
  challenge runs over port 80).

The installer then:

1. writes `RETICORA_TLS_DOMAIN` (and `RETICORA_CERT_EMAIL`) into `.env`,
2. creates a temporary **self-signed bootstrap certificate** under
   `deploy/docker-compose/letsencrypt/live/<domain>/` (valid 30 days) so
   nginx can start with the TLS configuration before the real certificate
   exists,
3. starts the stack — the frontend's nginx serves the ACME challenge
   directory from the `certbot-webroot` compose volume and redirects all
   other HTTP traffic to HTTPS
   (`deploy/docker-compose/nginx-tls.conf.template`),
4. requests the real certificate with `certbot certonly --webroot` and
   reloads nginx.

The long-running `certbot` service in the compose file renews the
certificate automatically (`certbot renew` every 12 h; a no-op until the
certificate is close to expiry). Re-running the installer is safe: an
existing real certificate is kept and no new one is requested.

If issuance fails (e.g. DNS not yet propagated or ports blocked), the stack
keeps running with the self-signed certificate and the installer prints a
warning — fix the connectivity and re-run `./install-cloud.sh` to retry.

The TLS server configuration is rendered by the nginx image entrypoint from
`/etc/nginx/templates/reticora-tls.conf.template` (envsubst) into
`/etc/nginx/conf.d/reticora-tls.conf`, which the main `nginx.conf` baked into
the frontend image includes. With TLS enabled the UI is served on the
standard ports: `443` for HTTPS and `80` for the ACME challenge plus the
redirect to HTTPS (`RETICORA_FRONTEND_PORT` is set to `80` in `.env`). If
host port 80 is already taken (e.g. by another reverse proxy terminating TLS
in front of this stack), set `RETICORA_HTTP_BIND=127.0.0.1` in
`deploy/docker-compose/.env` to keep the plain-HTTP container port on
localhost only.

Also set the **public URLs** to the HTTPS address during installation:
`RETICORA_PUBLIC_BASE_URL=https://cmdb.example.com` (the installer suggests
the domain accordingly) and the matching OIDC issuer/redirect URLs.

### Troubleshooting: `container docker-compose-server-1 is unhealthy`

The API server refuses to start without a readable RS256 session signing key.
If the stack was ever started before the key existed (for example a plain
`docker compose up -d` or an installer run that was aborted early), Docker
creates a root-owned **directory** at
`deploy/docker-compose/secrets/session-private.pem` — the bind-mount source it
could not find. The server then exits with
`read session key … is a directory`, its health check never passes and Compose
aborts the dependent frontend with
`dependency failed to start: container docker-compose-server-1 is unhealthy`.

Re-running `./install-cloud.sh` repairs this automatically: it detects the
placeholder directory (or an empty key file), removes it and regenerates the
key. If the directory cannot be removed because it is root-owned, delete it
manually and re-run the installer:

```bash
sudo rm -rf deploy/docker-compose/secrets/session-private.pem
./install-cloud.sh
```

### Troubleshooting: `502 Bad Gateway` on the Keycloak login screen

Clicking "Anmelden" redirects the browser to
`<base URL>/realms/reticora/protocol/openid-connect/auth`, which the web UI's
nginx proxies to the Keycloak container. A **502 Bad Gateway** there means
Keycloak is not running — check with `docker compose ps` whether the
`keycloak` service is `Restarting`, and inspect `docker compose logs keycloak`.

The known cause is a realm import failure:

```
ERROR: java.nio.file.AccessDeniedException: /opt/keycloak/bin/../data/import/realm-reticora.json
```

The rendered realm at `deploy/docker-compose/.generated/realm-reticora.json`
is bind-mounted into the container, where the Keycloak process runs as the
unprivileged `keycloak` user (uid 1000). If the file is only readable by its
owner on the host (e.g. mode `600` from an older installer version), the
container cannot read it, the import aborts and Keycloak crash-loops.
Re-running `./install-cloud.sh` repairs the permissions automatically (the
file becomes group-readable for the container, or world-readable as a
fallback when the installer cannot change the group). Manual alternative:

```bash
chmod 644 deploy/docker-compose/.generated/realm-reticora.json
docker compose --env-file deploy/docker-compose/.env -f deploy/docker-compose/docker-compose.yml up -d keycloak
```

## 2. Collector VM (customer network)

```bash
sudo ./install-vm.sh                      # systemd mode (default)
sudo ./install-vm.sh --docker             # Docker mode
sudo ./install-vm.sh --release v1.2.3     # a named release (default: the newest)
sudo ./install-vm.sh --build-from-source  # local build, not signature-verified
```

The installer asks for:

- **Cloud connection** — URL of the central Reticora cloud, organization ID
  (tenant UUID from the cloud UI) and a unique collector ID (default:
  hostname).
- **Discovery scope** — subnets to scan (comma-separated CIDR), discovery
  protocols (`sweep, snmp, ssh, wmi, ipmi, redfish, power, nas`), discovery
  and heartbeat intervals.
- **Device credentials** — SNMP community, optional SSH username/password used
  by the discovery plugins.
- **Offline buffer** — spool directory where results are kept while the cloud
  is unreachable.
- **mTLS identity** — client certificate/key/CA files, or empty to use the
  enrollment keystore at `/opt/reticora-collector/credentials.json`.

It then writes `/opt/reticora-collector/collector.env` (mode `600`), installs
the signed collector of the release — the binary `reticora-collector-linux-<arch>`
after `cosign verify-blob` and its sha256 in the signed `SHA256SUMS`, or with
`--docker` the image `reticora-collector` after `cosign verify`, pinned by
digest (Docker/Compose are installed when missing) — installs and starts a
`reticora-collector.service` systemd unit — or, with `--docker`, a minimal
compose project in `/opt/reticora-collector/` — and verifies that the
collector is running.

With `--build-from-source` (`RETICORA_BUILD_FROM_SOURCE=1`) it builds the
collector locally instead, not signature-verified: it downloads the repository
sources when the script runs outside a checkout (`RETICORA_REPO_REF`, default
`main`; a release tag is checked against the signed release), installs the Go
toolchain matching `go.work` into `/usr/local/go` when the local one is missing
or too old (checked against the sha256 of go.dev), and builds the binary (or
reuses `bin/collector` / an existing installation) or the image.

### Device certificates (Redfish, WMI, NAS)

The Redfish plugin (HTTPS 443), the WMI plugin (WinRM over HTTPS, port
5986) and the NAS plugin (Synology DSM over HTTPS, port 5001; TrueNAS over
HTTPS) verify the device certificate before they send any credential
(COL-02): by default against the system roots, and the certificate must name
the target address. WMI never uses plain-HTTP WinRM (5985), and there is no
option to allow it; enable the WinRM HTTPS listener on the Windows hosts
(for example `winrm quickconfig -transport:https` with a certificate of the
internal CA, or by group policy). The NAS plugin never contacts the
plain-HTTP DSM port 5000; it logs in with a POST that carries account and
password in the body (credential kind `nas`), so enable HTTPS in DSM. No
plugin sends credentials to a target whose certificate is not trusted, and
none follows a redirect; for WMI and NAS the scope run reports such a target
with the error `tls_untrusted`.

Devices with self-signed or internal certificates are trusted per network
through an exception in `collector.env`:

| Variable | Plugin |
| --- | --- |
| `RETICORA_REDFISH_TLS_SCOPES` | Redfish |
| `RETICORA_WMI_TLS_SCOPES` | WMI |
| `RETICORA_NAS_TLS_SCOPES` | NAS |

All use the same format: entries separated by `;`, each
`<cidr>=<rule>[,<rule>…]` with the rules

- `ca:<path>` — PEM bundle of the CA that issued the device certificates;
  chain and target address are verified against it instead of the system
  roots;
- `pin:<hex>` — SHA-256 of the SubjectPublicKeyInfo of the device
  certificate (colons allowed); for self-signed certificates the pin is the
  trust anchor. A pin together with `ca:` must match in addition.

```bash
RETICORA_WMI_TLS_SCOPES=10.0.10.0/24=ca:/etc/reticora/winrm-ca.pem;10.0.20.5/32=pin:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
```

The first entry whose network contains the target applies; targets outside
every entry use the system roots. A malformed value is ignored as a whole (the
collector logs an error) and only the system roots apply. The pin of a host is
printed by

```bash
openssl s_client -connect 10.0.20.5:5986 </dev/null 2>/dev/null \
  | openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | sha256sum
```

## 3. Kubernetes (Kustomize)

`deploy/k8s/` contains the server deployment (`base`) and the `staging` and
`prod` overlays. The manifests create neither the secrets nor the ConfigMap
`reticora-config`; create them in the namespace of the overlay (`reticora` for
`base` and `prod`, `reticora-staging` for `staging`) before the first rollout.

**Required secrets and configuration.** Outside development the server's start
check requires `RETICORA_DATABASE_URL`, `RETICORA_REDIS_URL`,
`RETICORA_MASTER_KEY` and `RETICORA_OIDC_ISSUER_URL`
(`requiredOutsideDevelopment` in `backend/internal/config/config.go`) and exits
without one of them. The login also needs the OIDC client, and invitations need
the accept page: without `RETICORA_INVITATION_ACCEPT_URL`, `POST /invitations`
and `POST /invitations/{id}/renew` answer 503. The base provides all of them,
`staging` and `prod` inherit them (staging only patches
`RETICORA_ENVIRONMENT=staging`, replicas and resources and drops the HPA). Every
reference is mandatory (`optional: false`): a missing secret, ConfigMap or key
keeps the pod in `CreateContainerConfigError`.

| Variable | Source (base, inherited by `staging` and `prod`) |
|---|---|
| `RETICORA_DATABASE_URL` | secret `reticora-db-runtime`, key `database-url` (runtime login, see below) |
| `RETICORA_REDIS_URL` | literal `redis://redis:6379` in `base/deployment.yaml` |
| `RETICORA_MASTER_KEY` | secret `reticora-secrets`, key `master-key` |
| `RETICORA_OIDC_ISSUER_URL` | ConfigMap `reticora-config`, key `oidc-issuer-url` (Keycloak realm, reachable from the users' browsers) |
| `RETICORA_OIDC_CLIENT_ID` | ConfigMap `reticora-config`, key `oidc-client-id` |
| `RETICORA_OIDC_CLIENT_SECRET` | secret `reticora-secrets`, key `oidc-client-secret` |
| `RETICORA_OIDC_REDIRECT_URL` | ConfigMap `reticora-config`, key `oidc-redirect-url` (`https://<host>/auth/callback`) |
| `RETICORA_INVITATION_ACCEPT_URL` | ConfigMap `reticora-config`, key `invitation-accept-url` (`https://<host>/invitations/accept`) |
| `RETICORA_SESSION_KEY_PATH` | secret `reticora-session-key`, key `private.pem`, mounted at `/etc/reticora/session` |

`prod` additionally includes the components `opensearch`, `backup` and
`nats-edge`, which need: secret `reticora-backup` (keys `database-url`,
`aws-access-key-id`, `aws-secret-access-key`), secret `reticora-pgbackrest`
(keys `cipher-pass`, `s3-key`, `s3-key-secret`, `ca.crt`, `tls.crt`,
`tls.key`), optionally secret `reticora-opensearch` (`username`, `password`,
only with the security plugin), a ReadWriteMany storage class for the PVC
`reticora-collector-pki`, and the host names of the collector endpoint in the
shipped ConfigMap `reticora-nats-edge` (key `hosts`, replace
`nats.example.com`).

`reticora-config` is provided by the operator like the secrets and is
deliberately not generated with placeholders: its values differ for every
installation, and a placeholder issuer or accept URL would pass the start
check but break the login and the links of the invitation mails.

The database needs two logins with separate secrets (TEN-03):

- **Migration login** (secret `reticora-db-migrate`, key `database-url`): the
  owner/superuser that runs `golang-migrate` on `backend/migrations/` before a
  rollout. No manifest references it; keep it out of the server's namespace if
  migrations run elsewhere.
- **Runtime login** (secret `reticora-db-runtime`, key `database-url`): the
  only DSN the server pod receives. It must name a login **without SUPERUSER
  and BYPASSRLS** that is a member of `reticora_app` and of no other role.
  Create it with the migration login after the first migration run
  (`reticora_app` comes from migration `000056`):

  ```sql
  CREATE ROLE reticora_runtime LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB
    NOCREATEROLE NOREPLICATION PASSWORD '…';
  GRANT reticora_app TO reticora_runtime;
  ```

```bash
kubectl -n reticora create secret generic reticora-db-migrate \
  --from-literal=database-url='postgres://reticora:…@db:5432/reticora?sslmode=require'
kubectl -n reticora create secret generic reticora-db-runtime \
  --from-literal=database-url='postgres://reticora_runtime:…@db:5432/reticora?sslmode=require&application_name=reticora-server'
kubectl -n reticora create secret generic reticora-secrets \
  --from-literal=master-key="$(openssl rand -base64 32)" \
  --from-literal=oidc-client-secret='…'
kubectl -n reticora create secret generic reticora-session-key \
  --from-file=private.pem=session-private.pem
kubectl -n reticora create configmap reticora-config \
  --from-literal=oidc-issuer-url='https://cmdb.example.com/realms/reticora' \
  --from-literal=oidc-client-id='reticora-app' \
  --from-literal=oidc-redirect-url='https://cmdb.example.com/auth/callback' \
  --from-literal=invitation-accept-url='https://cmdb.example.com/invitations/accept'
```

An existing deployment that still has `reticora-secrets/database-url` keeps
the old pods running but cannot roll out new ones until
`reticora-db-runtime` exists (`CreateContainerConfigError`); create the
runtime login and its secret first.

**TimescaleDB extension update (before the rollout).** A database keeps the
TimescaleDB extension version it was created with when its server gets a
newer TimescaleDB (image or package). Before the migrations of a rollout,
raise it with the migration login (the bootstrap superuser, which owns the
extension and the event trigger `reticora_restrict_app_ddl`). The script
disables that trigger only around `ALTER EXTENSION timescaledb UPDATE`, which
it otherwise breaks (`FATAL: "timescaledb" already loaded with a different
version`), enables it again also on failure, and changes nothing when the
extension is current. Stop the server first: while the trigger is disabled,
the application roles are not restricted to index DDL, so the script refuses
the update while a login of `reticora_app` or `reticora_owner` is still
connected.

```bash
kubectl -n reticora scale deployment/reticora-server --replicas=0
PGPASSWORD='…' PGSSLMODE=require \
  scripts/ops/timescaledb-update.sh --database reticora -- psql -h db.example.org -p 5432 -U reticora
migrate -path backend/migrations -database "$MIGRATION_DATABASE_URL" up
kubectl -n reticora apply -k deploy/k8s/overlays/prod   # sets the replicas again
```

A managed PostgreSQL whose administrator login cannot run `ALTER EVENT
TRIGGER` follows the provider's extension upgrade instead; afterwards
`SELECT evtenabled FROM pg_event_trigger WHERE evtname =
'reticora_restrict_app_ddl'` must return `O`. The server refuses to start, and
`/readyz` reports `database: unavailable`, while the trigger is disabled or
missing; an extension below the version of its installed library is logged
as a warning at start (OPS-01).

`RETICORA_MASTER_KEY` (envelope encryption of credentials, OPS-03) is
mandatory: the deployment references `reticora-secrets/master-key` without
`optional`, so a missing key keeps the pod in `CreateContainerConfigError`,
and the server's start check refuses an empty key. Keep the master key in a
backup outside the cluster; without it, stored credentials cannot be
decrypted.

**Images.** `prod` uses the signed release images
`ghcr.io/datahub-chiemgau/reticora-server` and
`ghcr.io/datahub-chiemgau/reticora-postgres` (pgBackRest CronJob) with the
release tag (`newTag: v1.0.0`); raise `newTag` in
`deploy/k8s/overlays/prod/kustomization.yaml` with each release. NATS,
OpenSearch and the TimescaleDB image of the `pg_dump` CronJob are upstream
images that the release does not build. `staging` uses
`reticora-cmdb/server:staging`, an image the operator builds and pushes.

CI (`k8s-manifests`) builds base and both overlays and asserts the master-key
secret reference, the required configuration
(`backend/internal/config/k8s_required_env_test.go`: every key above in base,
staging and prod, the server's start check with the rendered env, fixture
overlays without a key are reported) and the image origin (every Reticora image
of the prod build is a release image `ghcr.io/datahub-chiemgau/reticora-*` with
a tag `v*`).

## Layout

```
install.sh                        entry point (mode picker)
install-cloud.sh                  central-cloud installer
install-vm.sh                     customer-VM collector installer
deploy/
├── docker-compose/
│   ├── docker-compose.yml            full stack (env-driven, no secrets in git)
│   ├── docker-compose.override.yml   source-build config (tracked)
│   ├── nginx-tls.conf.template       TLS nginx config (used when RETICORA_TLS_DOMAIN is set)
│   ├── .env                          generated by install-cloud.sh (git-ignored)
│   ├── .generated/                   rendered Keycloak realm (git-ignored)
│   ├── letsencrypt/                  Let's Encrypt certificates (git-ignored)
│   └── secrets/                      session signing key (git-ignored)
├── keycloak/realm-reticora.json      realm template (secret placeholder)
├── k8s/                              Kustomize manifests
└── terraform/                        infrastructure as code
```
