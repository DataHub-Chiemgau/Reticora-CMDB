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

## 1. Central cloud

```bash
./install-cloud.sh
```

The installer asks for:

- **Passwords/secrets** — database, MinIO/S3, Keycloak admin, Keycloak DB,
  OIDC client secret, master key for credential encryption. Secure random
  defaults are generated automatically and reused on re-runs.
- **Public URLs** — web UI base URL, OIDC issuer, OIDC redirect URL. The
  **OIDC issuer URL is handed to the browser** as the Keycloak sign-in
  endpoint, so it must be reachable from your users' machines. Do **not**
  leave it at `http://localhost:8180/...` unless the browser runs on the
  server itself — otherwise sign-in fails with a "cannot reach localhost"
  error. The installer derives a matching default from the public base URL.
- **HTTPS / Let's Encrypt (optional)** — a public domain name (e.g.
  `cmdb.example.com`) and an e-mail address for certificate expiry notices.
  When a domain is entered, the installer automatically obtains a free Let's
  Encrypt certificate (nginx + Certbot, ACME http-01 challenge) and enables
  HTTPS with automatic renewal. Leave empty to keep plain HTTP.
- **Ports** — published host ports for UI (default 3000; fixed to 80 when
  HTTPS is enabled — port 80 answers the ACME challenge and redirects to
  HTTPS, the UI itself is then served at `https://<domain>` on port 443),
  API (8080) and Keycloak (8180).
- **Image source** — build from source (default) or pull prebuilt images from
  a registry.

It then:

1. checks the prerequisites (Docker + Compose, openssl, curl) and offers to
   install Docker if missing,
2. writes `deploy/docker-compose/.env` (mode `600`),
3. generates the RS256 session signing key at
   `deploy/docker-compose/secrets/session-private.pem` (group-readable by the
   container's `nobody` group so the server process can read it; the file is
   bind-mounted into the server container),
4. renders the Keycloak realm with the configured OIDC client secret into
   `deploy/docker-compose/.generated/`,
5. builds or pulls the server/frontend images,
6. starts the infrastructure and applies the SQL migrations from
   `backend/migrations/` (via `golang-migrate` when installed, otherwise via
   `psql` inside the postgres container; applied versions are tracked in the
   `reticora_schema_migrations` table so re-runs are no-ops),
7. starts Keycloak, the API server and the frontend,
8. when a TLS domain is configured: requests the Let's Encrypt certificate
   with Certbot (webroot challenge served by nginx) and reloads nginx,
9. verifies `/healthz` on the API,
10. optionally garbage-collects superseded container images (keeps the newest
    `RETICORA_GC_KEEP_IMAGES`, default 3, per image).

Afterwards the web UI is reachable at the configured base URL. The initial
login is `admin@reticora.local` / `admin123` — **change it immediately**.

Non-interactive (CI) usage: `./install-cloud.sh --non-interactive` uses the
existing `.env` values, generated defaults and never prompts.

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
   `deploy/docker-compose/letsencrypt/live/<domain>/` so nginx can start
   with the TLS configuration before the real certificate exists,
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

## 2. Collector VM (customer network)

```bash
sudo ./install-vm.sh            # systemd mode (default)
sudo ./install-vm.sh --docker   # Docker mode
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

It then writes `/opt/reticora-collector/collector.env` (mode `600`), builds
the collector binary from source (or reuses `bin/collector` / an existing
installation), installs and starts a `reticora-collector.service` systemd unit
— or, with `--docker`, a minimal compose project in
`/opt/reticora-collector/` — and verifies that the collector is running.

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
