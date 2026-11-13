# Reticora CMDB Documentation

## Architecture

Reticora CMDB is a multi-tenant Configuration Management Database designed for Managed Service Providers (MSPs) and Internet Service Providers (ISPs).

### Technology Stack

- **Backend**: Go 1.26.11+, chi v5 router, pgx v5 (hand-written SQL)
- **Frontend**: React 19, TypeScript, Vite, TanStack Query v5, Zustand, React Router v7
- **Database**: PostgreSQL 16 with Row-Level Security
- **Messaging**: NATS JetStream
- **Cache**: Redis 7
- **Observability**: OpenTelemetry, Prometheus metrics

### Installation requirements

`install.sh` supports Ubuntu 22.04 LTS, Ubuntu 24.04 LTS and Debian 12 with
Docker Engine ≥ 24 and Docker Compose v2, and refuses other systems. The
minimum for the central cloud is 4 vCPU, 8 GB RAM and 100 GB SSD for up to
10,000 objects; the sizing table up to 50,000 objects is in
[ops/sizing.md](ops/sizing.md). Installation problems:
[troubleshooting-installation.md](troubleshooting-installation.md).

By default the installers install the signed release artifacts of the newest
release (or `--ref`/`--release v<version>`), verified with cosign before
anything runs (TEC-15, E-69 Option 1). A development checkout is installed
with `./install.sh --cloud --build-from-source` (`RETICORA_BUILD_FROM_SOURCE=1`):
built locally, not signature-verified; only then is a ref that is no release
tag accepted. Default, switch and error messages:
[troubleshooting-installation.md, section 9](troubleshooting-installation.md#9-signature-verification).

### Monorepo Structure

```
├── api/                    # OpenAPI 3.1 specification
├── backend/
│   ├── cmd/
│   │   ├── server/         # Main API server
│   │   ├── collector/      # Discovery collector
│   │   └── agent/          # Edge agent (on-prem)
│   ├── internal/
│   │   ├── platform/       # Cross-cutting infrastructure
│   │   │   ├── httpx/      # HTTP utilities
│   │   │   ├── events/     # NATS event publishing
│   │   │   ├── audit/      # Audit log writing
│   │   │   ├── blob/       # MinIO/S3 blob storage
│   │   │   └── telemetry/  # OpenTelemetry setup
│   │   ├── ci/             # Configuration Items domain
│   │   ├── relationship/   # CI relationships
│   │   ├── discovery/      # Network discovery
│   │   ├── webhook/        # Webhook subscriptions
│   │   ├── tenant/         # Multi-tenancy
│   │   └── ...
│   └── migrations/         # golang-migrate SQL files
├── frontend/               # React SPA
├── deploy/
│   ├── k8s/                # Kustomize manifests
│   └── keycloak/           # Keycloak realm config
├── docs/                   # Documentation
├── docker-compose.yml      # Local development
└── Makefile                # Build automation
```

### Multi-Tenancy

All data is isolated per organization using PostgreSQL Row-Level Security (RLS).
The hierarchy is: Organization → Client → Site → Building → Room → Rack.

> **Deployment requirement — non-privileged database role.** RLS is bypassed for
> superusers and roles with `BYPASSRLS`. The server must therefore connect with a
> dedicated login created with `NOSUPERUSER NOBYPASSRLS` that is a member of the
> application role `reticora_app` (created by the migrations) and of no other
> role, e.g.:
>
> ```sql
> CREATE ROLE reticora_runtime LOGIN PASSWORD '…' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
> GRANT reticora_app TO reticora_runtime;
> ```
>
> The server refuses to start with a superuser or `BYPASSRLS` login, or with a
> login that reaches such a role (see `RETICORA_DATABASE_URL` below); the
> superuser of the `docker-compose.yml` development database is for migrations
> and the seed only. The
> `metric_sample` hypertable is the documented exception: TimescaleDB does not
> support RLS on hypertables with columnstore, so metrics isolation is enforced
> at the repository layer (every query carries `organization_id`).

### Migrations/Schema

Database schema is managed with [golang-migrate](https://github.com/golang-migrate/migrate). Migration files live in `backend/migrations/` using sequential numbering (`000001_`, `000002_`, …).

**Conventions:**

- Each migration has an `.up.sql` and `.down.sql` file.
- Tables use UUID primary keys (`gen_random_uuid()`).
- Every tenant-scoped table includes `organization_id UUID NOT NULL` with Row-Level Security policies.
- Timestamps use `TIMESTAMPTZ` (`created_at`, `updated_at`).
- Flexible/heterogeneous attributes are stored as `JSONB`.
- TimescaleDB hypertables are used for time-series data (e.g., `metric_sample`) with compression after 7 days and a retention policy of 400 days.
- Continuous aggregates provide hourly rollups for metrics.

**Current migrations (overview):**

| # | Name | Purpose |
|---|------|---------|
| 0001 | tenant_model | Organizations, clients, sites, buildings, rooms, racks |
| 0002 | ci_type_system | Configuration Items, types, attributes |
| 0003 | audit_log | Immutable audit trail with hash chain |
| 0004 | entitlements | Feature flags and license limits per tenant |
| 0005 | users_roles | Users, roles, permissions (RBAC) |
| 0006 | ci_relationships | Relationships between CIs |
| 0007 | webhooks | Webhook subscriptions and deliveries |
| 0008 | discovery | Discovery jobs, credentials, scan results |
| 0009–0014 | phase2_* | Assets, assignments, documents, stocktake, tickets, teams |
| 0015 | network_ipam | IP address management |
| 0016 | contacts | Contact entities |
| 0017 | export_jobs | Async CSV/DATEV export |
| 0018 | force_rls_triggers | Enforce RLS on all tenant tables |
| 0019 | api_keys_invitations | API key management and user invitations |
| 0020 | metrics | TimescaleDB metric_sample hypertable (retention: 400 days) |
| 0021 | spec_alignment | Schema alignment with latest spec |
| 0022 | ci_change | CI change history feeding the audit trail |
| 0023 | rls_variable_unification | One RLS session variable (`app.org_id`), legacy `ci.last_seen`/`ci.source` removed, identity-resolution indexes |
| 0024 | durable_webhook_delivery | Retry state (`status`, `max_attempts`, `next_retry_at`, `error`) and due-delivery index on `webhook_delivery` |
| 0025 | reconciliation_topology | Discovery job runs, the reconciliation `review_item` queue, relationship suppressions and the `powered_by` relationship type |
| 0026 | permissions_sla | First-class permission catalogue/role grants and SLA policy plus per-ticket clock state |
| 0027 | workflows_forms | JSON-Schema form definitions/submissions and workflow definitions, runs and step history |
| 0028 | compliance | Compliance rules and per-CI evaluation results |
| 0029 | iga | IGA connectors, provisioning tasks, access requests/reviews and drift findings |
| 0030 | search_ai | Tenant search index, AI conversations/messages and retrieval chunks |
| 0031–0033 | schema corrections, role seeds, client_scope RLS | Batch 1–2 hardening |
| 0034 | webhook_dead_letter | Dead-letter queue for exhausted webhook deliveries |
| 0035 | export_job_formats | `datev` in the export_job format CHECK; `app.system` worker exception |
| 0036 | updated_at_triggers | `set_updated_at` trigger on every mutable table that carries `updated_at` |
| 0037 | alert_rule | Persistent monitoring alert rules incl. pending/fired evaluation state |

**Running migrations:**

```bash
# Apply all pending migrations
migrate -path backend/migrations -database "$DATABASE_URL" up

# Rollback last migration
migrate -path backend/migrations -database "$DATABASE_URL" down 1

# Verify the full up/down/up round-trip (also enforced by the `migrations` CI job)
make migrate-roundtrip
```

**Tenant isolation:** every RLS policy reads the transaction-local session
variable `app.org_id`, which repositories set with
`SELECT set_config('app.org_id', $1, true)` before any tenant-scoped statement.
Migration 0023 unified the three variable names that had accumulated
(`app.organization_id`, `app.current_org`, `app.org_id`) onto `app.org_id`, added
the missing `WITH CHECK` clauses and `FORCE ROW LEVEL SECURITY`. The
`migrations` CI job fails if a policy reintroduces one of the legacy names or if
a table has RLS enabled but not forced.

The single exception is `webhook_delivery`: its policy additionally accepts
`current_setting('app.system', true) = 'on'` so that the retry worker can claim
due deliveries across tenants. That flag is set exclusively inside
`webhook.PGDeliveryStore.ClaimDue` and is never derived from request input.

### Backend Architektur

The backend is a **modularer Go-Monolith** built with strict Bounded-Context separation.

**Key libraries:**

| Library | Purpose |
|---------|---------|
| chi/v5 | HTTP router with middleware chain |
| pgx/v5 | PostgreSQL driver (connection pooling via puddle) |
| OpenTelemetry | Distributed tracing and metrics |
| NATS JetStream | Asynchronous event bus |

**Persistence:** every domain owns a hand-written `pg_repository.go` built on
pgx v5. Code generation with sqlc was removed because the generated package was
never populated and the two approaches drifted permanently; hand-written pgx is
now the single documented standard. The in-memory repositories exist only for
tests and the explicit `--no-db` development mode — the server refuses to start
when a database is expected but unreachable, and never silently degrades to
memory.

**Package layout (`backend/internal/`):**

```
internal/
├── api/            # Shared HTTP contract: pagination, cursors, envelopes
├── server/         # Single route-registration site (chi router assembly)
├── ci/             # Configuration Items domain (CRUD, search, bulk)
├── relationship/   # CI-to-CI relationships
├── topology/       # Derived network topology and neighbor queries
├── discovery/      # Network discovery, jobs and the reconciliation queue
├── tenant/         # Organization/client/site management
├── tenantapi/      # Clients, sites, buildings and rooms
├── rack/           # Racks, rack mounts and cabling
├── contact/        # Contacts and CI contact roles
├── ipam/           # Subnets, IP addresses and network interfaces
├── user/           # User accounts, authentication context
├── identity/       # OIDC/SCIM integration
├── webhook/        # Webhook subscription & dispatch
├── audit/          # Audit log writing (hash-chained)
├── asset/          # Asset lifecycle tracking
├── assignment/     # CI-to-user/team assignments
├── document/       # Document management
├── stocktake/      # Inventory counting
├── ticket/         # Ticket/issue tracking
├── permission/     # RBAC/ABAC permission catalogue and effective grants
├── sla/            # SLA policies and ticket clock state
├── form/           # JSON-Schema form definitions and submissions
├── workflow/       # Workflow definitions, deterministic executor and approvals
├── compliance/     # Compliance rules, evaluator and results
├── iga/            # IGA connectors, SCIM, JML, access reviews and drift
├── search/         # Postgres/OpenSearch full-text search backend
├── ai/             # OpenAI-compatible provider and governed RAG assistant
├── entitlement/    # License/feature-flag enforcement
├── export/         # Async export jobs (CSV, DATEV)
├── monitoring/     # Metric ingestion and queries
├── observability/  # OTel bootstrapping
├── platform/       # Cross-cutting: httpx, events, blob, telemetry
├── middleware/     # Auth, RLS context, rate-limit, request-ID
├── config/         # Environment-based configuration
├── database/       # Connection pool, migration runner
├── graphqlbff/     # GraphQL BFF layer for frontend
└── wire/           # Dependency injection wiring
```

**Request lifecycle:**

1. HTTP request → chi router → middleware chain (auth, org context, tracing).
2. Middleware sets `app.org_id` on the database session for RLS enforcement.
3. Handler calls domain service → repository (hand-written pgx queries in each domain's `pg_repository.go`).
4. Domain events published to NATS JetStream for async side-effects (webhooks, audit, cache invalidation).
5. Response serialized as JSON with RFC 7807 error format on failure.

**Authentication:** the SPA runs an OpenID Connect authorization-code flow with
PKCE. `GET /api/v1/auth/config` returns the public parameters (issuer, client
ID, redirect URI, scopes, endpoints) — never the client secret. The browser
redirects to the identity provider, and posts `code`, `state` and
`code_verifier` to `POST /api/v1/auth/callback`, which exchanges them for an
RS256 session token. The verifier is mandatory and the returned ID token is
signature-verified against the provider's JWKS with issuer, audience, expiry
and issued-at checks. The session token follows AUT-02: it lives 15 minutes,
carries `sub`, `org`, `scopes`, `cls`/`sts`/`tms` (client, site and team scope;
`null` = whole organization), `name`, `email`, `sid` (the session: the refresh
family of the login, kept by every refresh and counted by the per-session rate
limit), `jti`, `iat` and `exp`, and its header names the signing key in `kid`
(RFC 7638 thumbprint). Every subsequent request carries that token as a bearer
token in the `Authorization` header.
The refresh token is never visible to JavaScript: the callback sets it as the
HttpOnly, Secure, SameSite=Strict cookie `reticora_refresh` (path
`/api/v1/auth`), and the client calls `POST /api/v1/auth/refresh` once on a
401, which uses the cookie up, sets a rotated one and re-reads roles and
scopes; presenting a used cookie again revokes the whole session. Refresh
tokens, the logout blacklist (`POST /api/v1/auth/logout` revokes the refresh
family and blacklists the access token until it expires) and the per-user
deactivation blacklist live in Redis, so every replica sees them at once.
For a signing key rotation (SEC-06) set the new key in
`RETICORA_SESSION_KEY_PATH` and the old ones in
`RETICORA_SESSION_PREVIOUS_KEY_PATHS` (comma separated) for the overlap.
Server-side the token signature is always verified by
`middleware.AuthMiddlewareWithVerifier` — a missing `RETICORA_SESSION_KEY_PATH`
is a fatal startup error unless the operator explicitly opts into the insecure
development mode with `RETICORA_ALLOW_INSECURE_DEV_AUTH=true`. That development
login exists only with `RETICORA_ENVIRONMENT=development` (AUT-03): the start
check refuses it in staging and production, and without a verifier the
middleware rejects every session token outside development. Only
`/api/v1/auth/{config,callback,refresh,logout}` are unauthenticated.

**API keys (AUT-04):** `POST /api/v1/api-keys` (permission `apikey:manage`)
issues `rk_live_` or `rk_test_` keys: a 12 character Base62 prefix and a 40
character Base62 secret. The plaintext is returned once; only its SHA-256 is
stored and compared in constant time. A key cannot be given permissions its
creator lacks, and at every request it grants only the intersection of its
permissions with its owner's current role assignments, in the owner's scope.
`GET` lists keys without secrets, `DELETE /api/v1/api-keys/{id}` revokes at
once, and `POST /api/v1/api-keys/{id}/rotate` issues a successor while the old
key stays valid for the overlap (`overlap_seconds`, default 24 hours, at most
7 days). A presented key is identified by its prefix in a read-only system
transaction (`database.WithSystem`); everything else runs in the tenant
transaction of the key's organization. Keys issued before this format
(underscore between prefix and secret) remain valid until rotated.

**Operator path (SEC-07):** `/api/v1/admin/*` is outside the tenant API.
Operators log in with `POST /api/v1/admin/auth/callback` (OIDC code + PKCE);
only members of `RETICORA_OPERATOR_GROUP` (default `operators`) who used a
second factor (`amr`, or an `acr` from `RETICORA_OPERATOR_MFA_ACR`, default
`2`) get a 15-minute operator session, which the tenant API refuses.
`RETICORA_OPERATOR_TOKEN` (at least 32 characters, header `X-Operator-Token`)
is for bootstrap and break-glass only: every use logs a security alert and
counts in `reticora_operator_break_glass_total`. Every operator request,
refused ones included, is recorded in `operator_audit` with a hash chain of
its own (`GET /api/v1/admin/audit`, `GET /api/v1/admin/audit/verify`).

**Authorization and tenant resolution:** the auth middleware authenticates
session bearer tokens and `X-API-Key` service tokens and populates a single
authenticated principal (subject, organization, scopes, principal type) in the
request context. `TenantMiddleware` derives the organization exclusively from
that principal — request headers such as `X-Organization-ID` are never
consulted, so no caller can impersonate another tenant by changing a header.
Every route is wrapped with a permission middleware resolved from the route
table in `internal/server/authz.go`; routes without a mapping fail closed, and
a router test fails the build when a route is registered without a permission.
Rate limiting and idempotency are keyed off the principal as well, so spoofed
headers cannot reset rate budgets or collide across tenants; unauthenticated
endpoints are rate-limited per client IP and the rate limiter fails closed
when its cache is unavailable.

**Asset and CI (DB-05, CH12):** a CI can be linked to an asset
(`ci.asset_id`; `asset.ci_id` stays the asset's primary CI and links it). The
asset is canonical for serial number, location, physical lifecycle and
inventory data, which the CI shows read-only under `asset`. `ci.location_id`
mirrors `asset.location_id` (migration 0088 triggers) and follows a move of
the asset; writing the location of a linked CI is rejected with 422.
`ci.serial_number` stays the observed value for identity resolution; a
difference to the asset's serial number opens one review item
`serial_mismatch` per CI, and a manual serial write on a linked CI is
rejected.
Mounting a CI into a rack or moving it to another rack sets the location
(the asset's for a linked CI, otherwise the CI's) to the rack; unmounting
sets the rack's room. Every mount change writes `ci_change` (movement,
location_change) and an audit entry in the same transaction (RCK-03).
Physical containment (chassis → blade) is `ci.parent_ci_id`, which rejects
cycles. The structural relationships `contains`, `mounted_in` and
`located_in` are not stored but projected from `parent_ci_id`, `rack_mount`
and the CI's location (view `ci_structure_edge`, CH27); the relationship API
lists them with `projected: true` and rejects storing them (422). Topology and
impact traverse the projected containment. Migration 0089 turned stored
containment rows into `parent_ci_id` and moved the structural rows to
`migration_quarantine`.
`ci.lifecycle_state` (software lifecycle) exists only for CI types with a
lifecycle model and must be a state of it (trigger; a transition of a CI
without a model is 422 with the field `to_state`). `ci.health` is
read-only: every minute the worker `health` (also without the module
alerting) runs `ci_refresh_health`, which sets critical or warning when the latest
sample of a metric breaches an enabled rule of that severity, ok for samples
without a breach and unknown without a sample in the last 15 minutes
(migration 0090; health findings are not G1 scope, E-36).

**VLAN catalog (NET-08):** `vlan` (organization, optional site, number
1–4094, name, description) with `/api/v1/vlans`; the number is unique per site
and, for VLANs without site, per organization (two partial indexes, E-24). The
client follows the site and scopes the VLAN under RLS. `subnet.vlan_id` and
`network_interface.vlan_id` reference the catalog; migration 0091 turned the
former integer VLAN numbers of subnets into catalog entries.

**VRFs (TEN-10, CH10):** `vrf` per organization (name unique, description,
`is_default`) with `/api/v1/vrfs` (writes need `vrf:manage`). Every
organization gets exactly one default VRF `global` on creation (trigger on
`organization`, backfill in migration 0092); it cannot be deleted, nor can a
VRF with subnets. `subnet.vrf_id` is NOT NULL (default VRF when omitted) and
subnets are unique per (organization, VRF, CIDR); `ip_address.vrf_id` follows
the subnet and addresses are unique per (organization, VRF, address,
interface).

**Entitlement enforcement:** an organization is entitled exactly to its rows
in `entitlement` (ENT-05): a missing row means not entitled, only `cmdb_core`
is always active. At startup every organization without rows is provisioned
once with the features and default quotas of its plan (`organization.plan`;
`RETICORA_DEFAULT_PLAN` for the `--no-db` demo organization). The
entitlements are cached per organization for 60 seconds in the shared cache
(Redis), so a change on one instance invalidates every instance (ENT-03). The
middleware maps route prefixes to features (export, ingest including the
`/api/v1/discovery/ingest` alias, topology, ...) and answers with 403
`feature-not-entitled`; GraphQL fields check their feature as well (the
route→feature mapping of the phase-1 features is the table below). Quotas are
enforced in the domain service (`ci.Service.Create`) and answer with 403
`entitlement-limit`; `max_cis` counts the active CIs of the whole
organization, also those outside the writer's client and site scope
(`ci_active_count()`, migration 000186), and holds on every write
that makes a CI active: the restore, the unmerge, and of a review item `new`
and the merge of a `resurrected_device` item (the item stays open). After `valid_until` only discovery stops (CH21): ingest
and new scans answer 403 `license-expired` (counter
`reticora_ingest_refused_license_expired_total`), reading, editing, export and
every other feature stay available; the heartbeat response carries
`Reticora-License-Status` and the collector pauses without spooling until the
license is renewed. Over `max_cis` discovery keeps updating existing CIs,
but a new device becomes a review item `unlicensed_ci` holding the full device
record (once per device, counter `reticora_unlicensed_ci_total`) instead of a
CI; raising the limit adopts the waiting items as CIs, `create` adopts one
within the limit and `dismiss` discards it, both audited. A downgrade deletes
no data: only new CIs are blocked (REST) or held (ingest). Repository errors
deny access (fail-closed). The tenant API is read-only (`GET
/api/v1/entitlements`); entitlements are written by the operator only,
`POST /api/v1/admin/orgs/{id}/entitlements`, and every change is recorded
in `operator_audit` with the previous and the new state, in the same
transaction: without its entry the change is rolled back (500) (ENT-04,
E-12). The plan of an organization changes only through
`PUT /api/v1/admin/orgs/{id}/plan` (`{plan, reason}`, PKG-02, WP-882): in
one transaction `organization.plan` and `plan_changed_at` are set, every
feature of the new plan gets a row of origin `plan` (a disabled one is
enabled again), rows of origin `plan` outside the new plan are disabled
with their limits and `valid_until`, and all rows of origin `plan` take
the new plan (`entitlement.PlanTransition`). Rows of origin `addon` and
`manual` stay as they are; nothing is deleted (ENT-08). A quota of a plan
row that is missing or equal to the default of the old plan takes the
default of the new plan (no default: unlimited), an override stays. The
change is recorded as `plan.change` with before, after and reason in
`operator_audit`; `entitlement.changed` (with `plan_before`) is the last
write of the transaction and notifies the org_admins with
`entitlement:read`; the entitlement cache of every instance is dropped
after the commit. The current plan again answers 200 with `unchanged`,
without event. Add-ons (`iga`, `ai`, `admp`; CH14, PKG-03, WP-883) are
booked per organization independently of the plan:
`POST /api/v1/admin/orgs/{id}/addons/{key}` with an optional
`{valid_until, source}` stores the row with origin `addon`, enabled (201,
200 when booked again); `DELETE` cancels it (204): the row is disabled,
its limits and `valid_until` stay; an add-on that is not booked is 404, a
key that is no add-on 422 (`field` key). Both are recorded as
`addon.book`/`addon.cancel` with before and after in `operator_audit`
and emit `entitlement.changed` with `addon`; a plan change never touches
an add-on row. `GET /api/v1/admin/addons` lists the catalog
(`entitlement.AddonCatalog`: key, phase `A`, i18n key
`addons.<key>.description`). Only the feature keys of ENT-02, the phase-2 keys of ENT-06 (E-44),
the phase-3 keys (`monitoring`, `ipam`, `monitoring_network`,
`credential_vault`, `notifications_sms_pager`, `workflows`,
`compliance_findings`, `monitoring_access`, `monitoring_analytics`,
`compliance_automation`; E-151, WP-862), the module key of phase 4
(`endpoint_agent`) and the add-ons, and the quotas `max_cis`,
`max_collectors`, `max_users`, `max_api_keys`, `max_storage_mb`, the
monitoring quotas `max_monitored_targets`, `max_probes_per_minute`,
`min_probe_interval_s` and `max_targets_<type>` (WP-881) are
accepted (`entitlement.FeatureKeys`, `entitlement.Quotas`); any other key,
the former keys `inventory`, `stocktake`, `ticketing`, `workflow_forms` and
`compliance` among them, is 422 with the field. Migration 000284 renamed
`workflow_forms` to `workflows` and `compliance` to `compliance_findings`.

Route→feature mapping of the phase-1 features (ENT-02, ENT-05;
`featureRules`/`coreRoutes` in `backend/internal/entitlement/middleware.go`,
checks in the handlers where the middleware cannot see the object; F-400).
Not entitled is 403 `feature-not-entitled`; an expired discovery license
stops only ingest and new scans (CH21):

| Feature | REST (middleware) | In the handler / GraphQL / NATS |
|---------|-------------------|---------------------------------|
| discovery | `/api/v1/ingest*`, `/api/v1/discovery/*` (except the review alias), `/api/v1/discovery-scopes*`, `/api/v1/collectors*` (except `/collectors/enroll`), `/api/v1/credentials*` | resolving a `scope_suggested` review item (accept and dismiss) on `/review-items/{id}/resolve` and its alias; the collectors' edge ingest and their scope channel: without discovery `scopes.request`, `scopes.result`, `subnets.report`, `jobs.request` and `jobs.status` are refused, after expiry `scopes.result` and `subnets.report` |
| rack_view | `/api/v1/racks*`, `/api/v1/mounts*`, `/api/v1/rack-mounts*` | `POST /api/v1/locations` with kind `rack`, `PATCH`/`DELETE /api/v1/locations/{id}` of a rack node; GraphQL `rackLayout` |
| topology | `/api/v1/topology*`, `/api/v1/impact*`, `/api/v1/cis/{id}/dependencies`, `/api/v1/cis/{id}/blast-radius` | GraphQL `topology` |
| export_csv | `/api/v1/export*`, `/api/v1/exports*` | — |
| webhooks | `/api/v1/webhooks*` | — |
| api_access | `/api/v1/api-keys*` | every request authenticated with `X-API-Key` |
| cmdb_core (always) | everything else, among it the location tree (`GET /api/v1/locations*`, rack nodes with their names included, and writes of the other nodes) and the review queue (`/api/v1/review-items*`, the deprecated `/api/v1/discovery/review-items*`, GraphQL `reviewInbox`; CI merges open review items as well) | — |
Attribute rules use the JSON rule AST of `api/rules.schema.json` (MET-45,
phase 1: required, pattern, range, enum, combined with all).
`backend/internal/rules` evaluates them bindingly, `frontend/src/rules` for
form previews; both run the cases of `api/rules.conformance.json`.

Users join an organization by invitation (`/api/v1/invitations`, TLC-03,
`user:manage`): an invitation names the address, a role and its scope
(org, client or site within the issuer's own scope), is valid for seven
days and stores only the hash of its token, which the invitee receives by
mail as link (NTF-05).
`POST /api/v1/invitations/accept` (public, pre-auth limit) consumes it
atomically and once and creates the user with that role. With the Keycloak
admin client, which the server wires for invitations only together with the
central SMTP server (`RETICORA_SMTP_HOST`), the accepting person's password
creates a verified IdP account of the address; the token therefore reaches
the invited mailbox only. Issue and renewal send the invitation mail
straight to the invited address over the central SMTP server, in their
transaction and never over a notification channel of the organization (an
organization's own SMTP server could pass the link to its administrators;
organizations without an e-mail channel, like the default organization of
an installation, get it as well); neither their response nor
`invitation.created` carries the token or the link. When the SMTP server
does not take the mail, nothing is stored or renewed and the API answers
503 ("the invitation e-mail could not be sent"). Without that client the
response shows the token once, the organization's e-mail channels mail the
link of `invitation.created`, and the invitee signs in with an existing IdP
account of the address, which the first login links.
Expired invitations are renewed with a new token; pending ones can be
revoked. Every step is audited.
Operators create organizations with plan and limits through
`POST /api/v1/admin/orgs` (TLC-02): the same creation chain as the
self-signup (slug, default VRF, first org_admin, entitlements of the plan
with the given limits), atomically with an `org.create` entry in
`operator_audit`; with the Keycloak admin client and the central SMTP
server (`RETICORA_SMTP_HOST`, whose settings the installer also gives the
realm) the first org_admin is invited by the IdP's mail to set a password
(`admin_invited`), otherwise the organization is created without an IdP
account.
Plan matrix (ENT-06 in the version of E-44 and E-151, confirmed with E-34;
`planFeatures` in `backend/internal/entitlement/entitlement.go`):

| Plan | Features |
|------|----------|
| essential | cmdb_core, discovery, topology, rack_view, export_csv, webhooks, api_access, notifications_email, assets_basic, documents, import, ticketing_essential |
| standard | + ticketing_standard, signature, stock, stocktake_standard, barcode, gps_standard, workplace, export_datev, monitoring, ipam |
| pro | + internal_orders, monitoring_network, credential_vault, notifications_sms_pager, workflows, compliance_findings |
| enterprise | + monitoring_access, monitoring_analytics, compliance_automation, endpoint_agent |

`monitoring` from standard on deviates from ENT-06 (there enterprise only):
by decision of the client of 2026-11-09 standard holds the monitoring core
(reachability probes, state changes, notification, root-cause suppression,
flapping, maintenance windows, availability history); threshold rules,
recommendations and the network-protocol probes are `monitoring_network`
from pro on (E-151). IGA (`iga`), AI (`ai`) and the ADMP connection (`admp`)
are add-ons (CH14): no plan includes them, only their own entitlement row
unlocks them. `gps_standard` and `workplace` have
no module in phase 2 (LOC-09 and MGT-06 are P4); `ticketing_pro`,
`stocktake_pro`, `disposition`, `audit_extended` and `usage_tracking` are
not created. Every plan has default quotas (PKG-04, E-153, WP-881;
`entitlement.PlanQuotaDefaults`), stored in the limits of the feature's
row: essential / standard / pro / enterprise `max_cis` 500 / 2,500 /
10,000 / 50,000, `max_collectors` 2 / 5 / 20 / –, `max_users` 5 / 25 /
100 / –, `max_storage_mb` (MiB, on `documents`, MGT-01) 1,000 / 10,000 /
50,000 / –; on `monitoring` from standard on `max_monitored_targets`
500 / 5,000 / 25,000, `max_probes_per_minute` 1,000 / 10,000 / 30,000 and
`min_probe_interval_s` 30 / 30 / 10 (– is unlimited, no entry). The trial
stores the defaults of the trial plan (with its configured `max_cis`; after the trial the system job `entitlement.trial_end` changes to the fallback plan with `ResetLimits`, recorded in `operator_audit` under the operator kind `system`, migration 000290, and once per trial through `notice_sent`), the
operator creation (`POST /api/v1/admin/orgs`) the defaults unless
`limits` passes a value. A quota only gates when it is stored: existing
organizations keep their rows, so a quota without entry stays unlimited
until the operator stores one; `entitlement.LimitOrigin` tells a plan
default from an override. Monitoring quotas are admitted under the
advisory lock `database.LockQuota` (`Service.AdmitCount`), so concurrent
creations cannot pass the limit; an interval below `min_probe_interval_s`
is 422 on `interval_s` (`Service.CheckInterval`).
**Probe budget (PKG-04, NFR-11, WP-940):** the monitoring domain admits
every create, change and enable of a probe with
`entitlement.(*Service).AllowProbeChange(ctx, org, before, after)`; the
budgets come from `entitlement.BudgetOf` over the effective values of the
active probes: distinct targets (the CI, else the host of the target in
the probe's client) against `max_monitored_targets`, Σ 60/`interval_s`
exactly as a fraction against `max_probes_per_minute` (30 s are 2 checks
per minute), distinct targets per registry type against
`max_targets_<type>` and the shortest interval against
`min_probe_interval_s`. Lock order: the write transaction takes the budget
lock `database.LockQuota(ctx, org, "probe_budget")` exclusively first,
before every row lock, and counts both budgets in the same transaction
over every active probe of the organization (also those outside the
caller's scope). A create counts before its insert. A change takes, after
the budget lock, only the row lock of the changed probe (`probe` `FOR NO
KEY UPDATE`; the count then sees the row it changes as stored, and no
other write can change it before the commit) and counts before its update;
the advisory lock `probe_parent:<org>` follows it. A disabled probe needs
no budget lock: only the interval it asks for is checked.
Error mapping: a raise above a quota is 403 `entitlement-limit` naming the
quota and its limit, a shorter interval than the plan minimum 422 on
`interval_s`, also on a disabled probe (a create, an interval change and
an enable of a probe stored below the minimum); the limit is reachable
exactly. Only raises are checked: delete and disable never refuse, and stock above a quota after a plan
change keeps running. A stored interval below `min_probe_interval_s` runs
at the minimum: the resolution of the effective values marks it with
origin `plan` and `clamped`.
`gps_standard` gates the GPS history in the P4 module `gps`; disposal
records (MGT-02) are the P2 module `disposal` with `assets_basic`.
Migration 000190 moved existing
organizations to the matrix: `inventory` → `assets_basic` (and `stock` from
standard on), `ticketing` → `ticketing_essential` (and `ticketing_standard`
from standard on), `stocktake` → `stocktake_standard`, keeping plan,
enabled, limits, `valid_until` and source of the former row; every
organization with rows got the missing phase-2 keys of its plan.

**Org gate (WP-705, E-44, E-38, E-39):** the global middleware
`entitlement.(*Service).Middleware` is `RequireFeature` of ENT-03. It reads
one mapping table, `featureRules` in
`backend/internal/entitlement/middleware.go`: method (optional), prefix,
suffix (optional) → one or more feature keys. It is the only source of the
org gate and of the module test (`TestP2ModulePrefixesAreGated` in
`backend/internal/server/modules_test.go`: every prefix of a P2 module
except `metamodel` (E-105) has a key). `entitlement.RequiredFeature(method,
path)` checks suffix rules first, then method rules, then prefix rules; a
prefix matches whole segments, a suffix the sub-path of one resource
(`/api/v1/assets/{id}/label`, also `label.svg` and paths below it). A rule
with several keys needs every one (`RequiredFeatures`). The enrollment
endpoint `/api/v1/collectors/enroll` and `coreRoutes` stay ungated, a
request with an API key needs `api_access` on every path, and an expired
discovery license stops only `IsIngest` requests. The phase-2 mapping:

| Feature | REST (middleware) |
|---------|-------------------|
| assets_basic | `/api/v1/assets*`, `/api/v1/assignments*`, `/api/v1/reservations*`, `/api/v1/compositions*`, `/api/v1/disposal-records*`, `/api/v1/suppliers*`, `/api/v1/contracts*`, GET `/api/v1/stock-movements*` |
| barcode | `/api/v1/assets/{id}/label*` (together with assets_basic), `/api/v1/labels*` |
| signature | GET `/api/v1/stock-movements/{id}/signature` |
| gps_standard | `/api/v1/asset-locations*`, `/api/v1/assets/{id}/locations` |
| stock | `/api/v1/consumables*`, `/api/v1/inventory/*`, POST (and every method but GET) `/api/v1/stock-movements*` |
| stocktake_standard | `/api/v1/stocktakes*` |
| internal_orders | `/api/v1/orders*` |
| documents | `/api/v1/documents*`, `/api/v1/tickets/{id}/attachments` (together with ticketing_essential; without the release module `documents` the route is not registered, 404; SR-61, WP-874) |
| import | `/api/v1/imports*`, `/api/v1/import-profiles*` |
| ticketing_essential | `/api/v1/tickets*`, POST `/api/v1/review-items/{id}/ticket` (reading the review queue stays core) |
| ticketing_standard | `/api/v1/slas*`, `/api/v1/tickets/{id}/sla`, `/api/v1/business-calendars*` |

The phase-3 mapping (WP-865, E-151, E-160; `TestP3ModulePrefixesAreGated`:
every prefix of a P3 module except `org_lifecycle` and the provider callback
has a key):

| Feature | REST (middleware) |
|---------|-------------------|
| monitoring | `/api/v1/monitoring*` (probes, probe types, profiles, groups, events, alarms, incidents, alarm settings and playbooks, alert rules, metrics), `/api/v1/collector-groups*`, `/api/v1/cis/{id}/availability`, `/api/v1/credential-bindings*`, `/api/v1/cis/{id}/credentials*`, `/api/v1/maintenance-windows*` |
| monitoring_network | `/api/v1/monitoring/incidents/{id}/recommendation`, `/api/v1/cis/{id}/configs*`, `/api/v1/subnets/{id}/rpki` (together with ipam) |
| monitoring_analytics | `/api/v1/monitoring/sla*`, `/api/v1/monitoring/forecasts*` |
| ipam | `/api/v1/ipam*`, `/api/v1/ipam-sections*`, `/api/v1/asns*`, `/api/v1/asn-ranges*`, `/api/v1/bgp-peerings*`, `/api/v1/nat-translations*`, `/api/v1/nameserver-sets*`, `/api/v1/vlan-domains*`, `/api/v1/cis/{id}/addresses`, `/api/v1/subnets/{id}/scan` (together with monitoring) |
| credential_vault | `/api/v1/vault-connections*` |
| admp (add-on) | `/api/v1/admp*`, `/api/v1/cis/{id}/admp-connections`, `/api/v1/network-interfaces/{id}/admp` |
| notifications_sms_pager | `/api/v1/escalation-policies*`, `/api/v1/notification-escalations*` |
| workflows | `/api/v1/forms*`, `/api/v1/form-submissions*`, `/api/v1/workflows*`, `/api/v1/workflow-runs*` |
| compliance_findings | `/api/v1/compliance*`, `/api/v1/security*` |

Core without feature: `/api/v1/org/exports*` and `/api/v1/org/deletion`
(`coreRoutes`, TLC-05, TLC-06) and the public provider callback
`/api/v1/notification-callbacks*` (signature of the provider, WP-1146).
Functions without a path of their own are checked in handlers and workers:
`entitlement.ProbeTypeFeature` maps every key of the probe type registry
(`icmp`, `tcp`, `http`, `dns` → monitoring; `snmp_status`, `snmp_if`,
`snmp_custom`, `redfish`, `bgp`, `ospf`, `isis`, `bfd`, `optical`, `dhcp`,
`stamp`, `twamp_control`, `gnmi`, `rpki_rtr`, `rpki_roa` →
monitoring_network; `bng`, `pon`, `tr069`, `usp` → monitoring_access), and
`entitlement.SubFunctionFeature` the sub-functions (correlation →
monitoring; recommendation, threshold rules, configuration change detection
→ monitoring_network; SLA reports, capacity forecasts →
monitoring_analytics). Standard holds the monitoring core only (decision of
the client of 2026-11-09).
| workplace | `/api/v1/desks*`, `/api/v1/desk-bookings*` |

Prefixes without a route yet (`suppliers`, `contracts`, `labels`,
`business-calendars`, `imports`, `import-profiles`) have their entry in
advance (E-44). A sub-function below a module prefix uses the route
middleware `(*Service).RequireFeature(key)`; a check in the handler (a
`signature` in the body of assign, `format=datev`) uses the port
`entitlement.Checker` (`IsEnabled(ctx, orgID, key string) bool`, implemented
by `*Service`). A missing feature is 403 `feature-not-entitled`
(`api.ProblemFeatureNotEntitled`), an exhausted quota 403
`entitlement-limit` (`api.ProblemEntitlementLimit`, `AllowCreate` with
`api.WriteEntitlementError`). The dashboard hides a widget whose module is
off or whose feature key is not entitled (`widgetFeature` in
`frontend/src/pages/DashboardPage.tsx`). The module keys of
`RETICORA_MODULES` (`inventory`, `documents`, `stocktake`, `ticketing`,
...) are release switches, not feature keys, and stay unchanged.


**Permissions and SLA:** migration 0026 turns permissions into data instead of
leaving them as ad-hoc JSON strings. `permission` is the global catalogue,
`role_permission` grants catalogue entries to tenant roles, and the effective
permissions endpoint also folds in existing custom-role JSON grants so old data
continues to work while new code can validate against the catalogue. A
standard role grants its `role_permission` rows and its seed keys
(`role.permissions`, the RBA-02 matrix); `PUT /api/v1/roles/{id}/permissions`
may add keys but answers 409 (`standard-role-permissions`) when a seed key is
left out, and the role's permission list, that response and the route audit
show the union (F-398, WP-669). Narrower roles are custom roles. SLA policy
rows define priority-based response and resolution targets, while `ticket_sla`
stores the applied policy and clock outcomes. Ticket creation, first visible
response and resolution transitions update that state so breach listings are
computed from persisted timestamps, not from UI-only heuristics.
`business_calendar = true` on a policy computes due times in business minutes
(`sla.addTarget` in `internal/sla/calendar.go`): only Monday–Friday,
08:00–18:00 UTC count, so a 4-hour target set Friday 17:00 is due Monday
09:00. With the flag off, targets remain plain elapsed minutes. The calendar
is intentionally fixed in this first iteration; per-tenant time zones and
holiday tables are a follow-up that extends the calendar without changing
`addTarget`'s call sites.


**Workflow builder and forms:** migration 0027 adds `form_def`,
`form_submission`, `workflow_def`, `workflow_run` and `workflow_step`. Form
submissions are validated server-side against the stored JSON Schema before they
are persisted; the focused validator supports the subset used by Reticora forms
(type, required, enum, numeric/string bounds, pattern, nested objects and arrays)
and returns an RFC 7807 validation problem with field-level errors. Workflow
definitions carry a trigger, conditions and ordered actions. The executor creates
a run, evaluates conditions deterministically, records every step, and pauses on
`require_approval` until `/api/v1/workflow-runs/{id}/approval` approves or
rejects it. Side effects are routed through repositories or the webhook
dispatcher rather than ad-hoc HTTP calls.

**Compliance evaluation:** migration 0028 adds `compliance_rule` and
`compliance_result`. Rules target a CI type and contain a small expression over
CI columns or `attributes.*`. `POST /api/v1/compliance/evaluations` evaluates
active rules against tenant CIs, replaces stored results for the tenant and
returns scores by CI type plus the overall pass percentage. The score excludes
`not_applicable` checks from the denominator and stored failures are exposed to
the frontend compliance page.


**Identity Governance (IGA):** migration 0029 adds the enterprise-only IGA
module. Connector definitions live in `iga_connector`; outbound secrets are
referenced by `credential_id` and are decrypted only through the existing
envelope-encryption credential service. The connector framework supports direct
SCIM 2.0 outbound calls (`/Users`, `/Groups`, paging and PATCH) and a relay mode
that queues collector-facing discovery jobs for systems reachable only from a
customer network. The inbound SCIM service provider is exposed under
`/scim/v2/Users`, `/scim/v2/Groups` and `/scim/v2/ServiceProviderConfig`, mapped
to `app_user` and `team` while preserving RFC 7643/7644 envelopes and errors.
Joiner/mover/leaver policies resolve identity changes into asynchronous
`iga_provisioning_task` rows with retry/backoff history. Access requests can be
approved or rejected and record the workflow definition selected for approval;
recertification campaigns store `iga_access_review` and `iga_access_review_item`
decisions, with revoke decisions feeding provisioning tasks. Reconciliation reads
connector accounts, compares them with expected state, stores
`iga_drift_finding` rows and offers remediation tasks for orphan or missing
accounts.

**Durable webhook delivery:** every dispatch is persisted to `webhook_delivery`
before the first HTTP attempt and updated after each attempt. Failed attempts
are rescheduled with exponential backoff (30s base, capped at 1h, 5 attempts by
default); a background worker claims due deliveries with
`FOR UPDATE SKIP LOCKED` and a short lease, so several replicas can share the
queue and deliveries survive a restart. `GET /api/v1/webhooks/{id}/deliveries`
exposes the history. Without a delivery store (`--no-db`) the dispatcher falls
back to in-process retries.

**API contract parity:** `api/openapi.yaml` is the single source of truth for
the public HTTP API. Every route is registered in exactly one place —
`server.NewRouter` in `backend/internal/server` — and
`TestRoutesAndSpecificationAreInParity` walks the assembled chi router with
`chi.Walk`, loads the specification and fails if a route is undocumented **or**
if the specification documents an operation that is not registered. The check
runs in the normal `go test ./...` job, so the specification cannot drift away
from the implementation in either direction. It compares the profiles
`postgres` and `no-db` in the releases "all modules", "G1" (the test profile
without late modules: paths without `x-phase`) and "G2" (the default without
`RETICORA_MODULES`, the same as `P2`: paths without `x-phase` or with
`x-phase: P2`, WP-830) and "G3" (the phase value `P3`: paths without
`x-phase` or with `x-phase` P2 or P3, WP-863). The deliberate exceptions are `/metrics` (the Prometheus scrape
endpoint, not part of the tenant API), the undocumented GraphQL BFF under
`/bff/` (GQL-01) and, as the only exception inside the contract, operations
marked `x-status: planned` (E-38, WP-247).

`x-status: planned` marks the scaffold of the Phase 2 path contract: on the
path level it covers every operation of the path, on the operation level that
one operation; any other value fails `speccheck.Planned`. A planned operation
has `operationId`, tag, `summary`, path parameters, `Limit`/`Cursor` on
collection GETs, a 2xx response with only a `description` and its errors as
`#/components/responses/*` (problem+json with `trace_id`,
`TestPlannedOperationsUseProblemResponses`). The parity test leaves planned
operations out of the expectation and fails with "x-status: planned
entfernen" as soon as a route serves one. A work package that implements a
planned operation therefore replaces the scaffold with the request and
response schemas of its domain, removes `x-status: planned`, deletes scaffold
operations it does not implement and regenerates
`backend/internal/server/testdata/route_permissions.txt`. The work package
for each planned resource is listed in the path inventory of E-38
(`docs/plan/phase2/entscheidungen.md`).

Path inventory of phase 3 (WP-864, E-160): the scaffold below marks every
prefix of the P3 modules of WP-863 and the operator package management with
`x-status: planned`; the paths of P3 modules carry `x-phase: P3`, the
operator paths none. Feature keys follow E-151 (gates of the new prefixes:
WP-865), permissions WP-866 and WP-870 or the work package that registers
the route. Below `/api/v1/admp` only GET is documented except the account
management `/api/v1/admp/accounts` (`TestADMPPathsAreReadOnly`); the access
to ADMP itself is read-only (WP-926, WP-927).

| Prefix | Module | Feature key | Permission | Work packages |
|---|---|---|---|---|
| `/api/v1/monitoring/{targets,probes,probe-types,profiles,groups,events}` | `monitoring` | `monitoring` | `monitoring:read`/`monitoring:write` | WP-934 – WP-949, WP-975, WP-979 |
| `/api/v1/monitoring/{alarms,incidents,alarm-settings,alarm-playbooks}` | `monitoring` | `monitoring`; recommendation `monitoring_network` | `monitoring:read`; ack `alert:acknowledge` | WP-982 – WP-1043 |
| `/api/v1/monitoring/{sla,forecasts}` | `monitoring` | `monitoring_analytics` | `monitoring:read`/`monitoring:write` | WP-1048 – WP-1061 |
| `/api/v1/collector-groups` | `monitoring` | `monitoring` | `monitoring:read`/`collector:manage` | WP-947 |
| `/api/v1/cis/{id}/{availability,configs}` | `monitoring` | `monitoring`; configs `monitoring_network` | `monitoring:read` | WP-973, WP-1056 |
| `/api/v1/credential-bindings`, `/api/v1/cis/{id}/credentials` | `monitoring` | `monitoring` | `credential:read`/`credential:bind` | WP-890, WP-894 – WP-896 |
| `/api/v1/vault-connections` | `monitoring` | `credential_vault` | `credential:read`/`vault:manage` | WP-893 |
| `/api/v1/ipam`, `/api/v1/ipam-sections`, `/api/v1/asns`, `/api/v1/asn-ranges`, `/api/v1/bgp-peerings`, `/api/v1/nat-translations`, `/api/v1/nameserver-sets`, `/api/v1/vlan-domains`, `/api/v1/cis/{id}/addresses`, `/api/v1/subnets/{id}/{scan,rpki}` | `ipam` | `ipam`; rpki `monitoring_network` | `ipam:read`/`ipam:write` | WP-900 – WP-924, WP-1062 – WP-1072 |
| `/api/v1/admp`, `/api/v1/cis/{id}/admp-connections`, `/api/v1/network-interfaces/{id}/admp` | `admp` | `admp` (add-on) | `admp:read`; holder `admp:holder_read`; accounts `admp:manage` | WP-925 – WP-932, WP-1073 – WP-1078 |
| `/api/v1/escalation-policies`, `/api/v1/notification-escalations`, `/api/v1/notification-callbacks` | `escalation` | `notifications_sms_pager` | `escalation:manage`; ack `alert:acknowledge`; callback public (provider signature) | WP-1140 – WP-1148 |
| `/api/v1/org/exports`, `/api/v1/org/deletion` | `org_lifecycle` | none (core) | `org:export`, `org:delete` | WP-1149 – WP-1157 |
| `/api/v1/admin/orgs/{id}/{plan,addons,usage}` | none (operator) | none | operator login | WP-880 – WP-885 |

Deprecation follows API-01 (E-25, WP-167): the operation carries
`deprecated: true`, `x-deprecated-at`, `x-sunset` (at least 12 months later)
and `x-successor` (a plain path, filters go into `description`). The
middleware `OpenAPIValidation` derives the `Deprecation`, `Sunset` and
`Link: <…>; rel="successor-version"` headers from these fields
(`ParseDeprecations`); no handler sets them itself. An alias is a second
registration in the domain handler (WP-168). `x-successor` must be a
documented path with at least one operation that is not planned, and its
`x-phase` must be empty or that of the deprecated path, so every release that
serves the alias serves the successor too (`TestDeprecationSuccessorsAreServed`).

**Pagination:** list endpoints accept `limit` (default 50, maximum 200) plus
either `offset` for random access or `cursor` for stable forward iteration.
The cursor is an opaque base64url token that encodes the sort field, direction
and the `(sort value, id)` position of the last returned row. Repositories turn
it into a keyset predicate — `(sort_column, id) < ($1, $2)` — instead of an
`OFFSET`, so a page never skips or repeats rows when data is inserted between
requests, and the query cost stays constant on deep pages. A response that has
more data returns `next_cursor`; passing it back with a different `sort_by` or
`sort_dir` is rejected with HTTP 400, because the token is only meaningful for
the ordering it was issued for. `total` is always the absolute match count and
therefore ignores the cursor predicate. Cursors are supported by CIs, assets,
assignments, documents, stocktakes and tickets.

**Streaming export:** `GET /api/v1/cis/export` fetches rows in batches of 500
and writes them to the response as they arrive, so memory use is bounded by the
batch rather than by the result size. The first batch is fetched *before* the
status line is written, which keeps a failing query a clean HTTP 500 with an
RFC 7807 body. Once bytes are on the wire a later failure can no longer change
the status code, so the writer aborts without emitting the JSON terminator —
truncated output is detectable by the client instead of silently looking
complete.

**Asynchronous export jobs:** `POST /api/v1/export/jobs` queues an export in
the `export_job` table (migration 0017) instead of streaming it in the request.
A background worker shared by all replicas claims pending jobs
(`FOR UPDATE SKIP LOCKED`), renders the CI set in the requested format (CSV,
DATEV or JSON — the same row shape as the streaming endpoint) into object
storage, and marks the job completed with the object key, row count, size and a
24-hour expiry. `GET /api/v1/export/jobs` and `/api/v1/export/jobs/{id}` report
progress; a completed, unexpired job carries a `download_url` minted via
`blob.PresignedGetURL` (S3 SigV4, 15-minute TTL; a `file:` URL in `--no-db`
development mode). Failed jobs keep their error message; expired jobs no longer
expose a URL. When no blob store is configured the endpoint answers 503 and the
streaming export remains available.

**Full export of the organization (TLC-05, WP-1149):** the job kind
`org.export` (3 attempts, 2 h, cancellable) exports every table of the
organization as JSONL for portability and the data processing agreement.
`export.OrgTables` reads the catalog from `pg_catalog`: every table and
hypertable of `public` with `organization_id` plus the organization row, so
a new table is exported without a code change; only `orgExportExcluded` is
left out, each with a reason (`org_dek`, `pii_subject` — exported decrypted
as `pii_subject.jsonl` —, `search_document`, `search_document_field`).
Secret columns are never selected (suffix `_hash`, `_enc`, `_ciphertext`,
prefix `encrypted_` or `secret`, plus `orgExportSecretColumns`, e.g. the
credential ciphertext and the previous webhook secret). The job reads in one
tenant transaction `REPEATABLE READ, READ ONLY`
(`database.WithTenantSnapshot`), in batches of 5,000 rows by primary key
(without one through a cursor by `ctid`; `metric_sample` through its view
`metric_sample_v`), reveals `$pii` references with `pii.RevealTx` (E-165)
and writes `org/<org>/org-export/<job>/tables/<table>/part-<nnnn>.jsonl`
(at most 64 MiB per part) to the bucket `exports`. `manifest.json` next to
the parts and in the job result lists per table the rows, the parts with
key, bytes and SHA-256 and the snapshot; progress rises per table. A retry
after a crash keeps the completed tables and exports the rest in a new
snapshot; a cancel (JOB-03) stops before the next batch and removes the
objects of the job. Documents, routes and download follow in WP-1150 and
WP-1151.

**Privacy / GDPR:** `GET /api/v1/users/{id}/data-export` returns the full set
of personal data stored about a user (account record plus all contact records
carrying the user's e-mail address) — the Art. 15 access request.
`POST /api/v1/users/{id}/anonymize` implements the Art. 17 right to erasure:
e-mail, display name, avatar and external ID are replaced with deterministic,
non-reversible surrogate values (`deleted-<hash>@anonymized.invalid`) and the
account is deactivated, while the row itself is kept so foreign keys
(tickets, assignments, audit hash chain) stay intact. Self-anonymization is
rejected so an operator cannot lock themselves out mid-request. Both
endpoints require `user:manage`.

**Rights of a person (SEC-05 [P3], WP-1158):** `POST
/api/v1/privacy/subjects/report` and `POST /api/v1/privacy/subjects/erase`
(both `user:manage`, 202 with the job, `Location: /api/v1/jobs/{id}`) cover
every person, not only users: the body names exactly one of `user_id`,
`contact_id` or `subject_id` (a vault subject of kind person or contact, e.g.
the holder of an ADMP connection); a person of another organization is 404.
The match values are e-mail, name and phone of the account or of the
decrypted vault subject (lower case, trimmed). Personal values of the history
are value subjects without a blind index, so the jobs decrypt them in batches
of 1,000 and compare; a value anchored to the person's subject belongs to it
anyway. The job `privacy.subject_report` lists per finding table, row id and
field path: surrogate references in every jsonb column of the tenant tables
(audit changes, CI history, events and later tables without code change), the
user as actor, assignee or recipient (audit, CI history, stock movements,
deliveries, tickets, assignments), deliveries to the person's address, every
column with a foreign key on `pii_subject` (read from `pg_constraint`, e.g.
`contact.pii_subject_id`, `admp_connection.holder_subject_id`) and other
contacts with the same address. The values are unsealed in the result (E-165),
which only `user:manage` reads. `privacy.subject_erase` with `mode = shred`
deletes the value subjects (one transaction per batch) and the person's
subject (anchored ones such as an ADMP address by cascade), anonymizes a user
(sessions revoked), empties the address of deliveries to the person and the
results of earlier reports about it; every reference then resolves to null
(shown as "deleted"). `mode = pseudonymize` replaces the data of the found
subjects with `person-<8 hex digits of SHA-256 of the surrogate>` and keeps the
surrogates. Both jobs write one audit entry (`privacy.subject_report`,
`privacy.subject_erase`) with id or surrogate, mode and counts, never a value.

**Document content:** documents carry metadata rows; the binary content lives
in blob storage. `PUT /api/v1/documents/{id}/content` stores the request body
(limited to 25 MiB, Content-Type validated against a whitelist of common
office/image formats — HTML, scripts and SVG are refused because stored
attacker-controlled markup would be an XSS vector on our own origin) under a
server-generated key (`documents/<org>/<id>/<version>`), and
`GET /api/v1/documents/{id}/content` returns a presigned download URL
(15-minute TTL). Without a configured blob store both endpoints answer 503
while metadata CRUD keeps working.

**Stocktake completion:** `GET /api/v1/stocktakes/{id}/difference` returns the
scans that deviate from the expected inventory (`missing`, `surplus`,
`damaged`, `wrong_location`), each enriched with the affected asset when the
scan resolved to one. `POST /api/v1/stocktakes/{id}/complete` finalizes the
count and — unless `apply_corrections` is `false` — applies the recorded
differences to the inventory in the same transaction: missing assets are
marked `lost`, surplus assets return to `in_stock`, `wrong_location` moves the
asset to the found location and damaged assets go to `maintenance`. Scans
without a resolvable asset or without a found location are reported in the
response but skipped. Completing an already completed or cancelled stocktake
is rejected with 409, so corrections can never be applied twice.

**Reconciliation and topology:** discovery runs are recorded as jobs
(`/api/v1/discovery/jobs`). Findings that cannot be matched to an existing CI
with sufficient confidence are not applied directly; they land in the review
queue (`/api/v1/discovery/review`) where they are accepted or rejected by an
operator. Relationships derived from discovery evidence (LLDP/CDP neighbours,
hypervisor placement, power feeds) are written as regular
`ci_relationship` rows, and a suppression list keeps operator-rejected pairs
from being re-derived on the next run. `/api/v1/topology` and
`/api/v1/topology/cis/{id}/neighbors` serve the resulting graph.

**Search:** `GET /api/v1/search` searches CIs, assets, documents, tickets and
contacts within the current tenant and filters hits through the caller's
effective read permissions. PostgreSQL full-text search over `search_document`
is the default backend. Set `RETICORA_SEARCH_BACKEND=opensearch` together with
`RETICORA_OPENSEARCH_URL` to use OpenSearch; startup pings OpenSearch, applies
the index template idempotently (`_index_template/<index>`, explicit mapping
for `organization_id`/`entity_type`/`entity_id`/`title`/`summary`/`metadata`/
`updated_at`) and fails loudly if either step fails. User input is passed as
structured parameters (SQL bind variables or OpenSearch JSON DSL), never
interpolated into query strings.
`POST /api/v1/search/reindex` rebuilds the tenant index. The index is kept
fresh between rebuilds: the CI repository is wrapped by an indexing decorator
(`ci.NewIndexingRepository`) that mirrors every successful create, update and
delete into the search backend — on every write path, including collector bulk
ingest. Indexing is best-effort: a failing search backend is logged and never
fails the CI mutation, because the index can always be rebuilt via reindex.

**OpenSearch operations (Epic G2):** OpenSearch is an optional backend. With
Docker Compose enable the `opensearch` profile (`docker compose --profile
opensearch up -d`) and set `RETICORA_SEARCH_BACKEND=opensearch` for the
server; with Kubernetes include the `deploy/k8s/components/opensearch`
Kustomize component from an overlay to add the StatefulSet, headless Service
and the server env patch. On every startup the server applies the index
template and re-checks connectivity. PostgreSQL remains the source of truth:
the hybrid backend rebuilds OpenSearch from `search_document` on
`POST /api/v1/search/reindex`, so a lost or rebuilt index is recovered online
and tenant-scoped. The pinned version is OpenSearch 3.9.0 (Upd-02, before
2.19.7); after the upgrade the index is rebuilt from PostgreSQL rather than
taken over from a 2.x snapshot. See `docs/opensearch.md` for the reindex and
upgrade runbook and troubleshooting (yellow status, disk watermark,
unreachable backend).

**Collector connectivity:** the collector authenticates uploads with mTLS when
client-certificate material is available — from `RETICORA_TLS_CLIENT_CERT`/
`RETICORA_TLS_CLIENT_KEY` (or the `_FILE` variants), or from the enrollment
keystore at `RETICORA_CREDENTIALS_PATH` (default
`/var/lib/reticora-collector/credentials.json`). `install-vm.sh` writes that
path into `collector.env` and puts the command of the Discovery page's enroll
instruction on PATH as `/usr/local/bin/reticora-collector`, the command its
systemd unit starts; the image ships the binary under the same name as its
entrypoint. `reticora-collector enroll --server <url> --code <code>` thus
stores the identity where the service reads it. Unattended deployments
(containers, automation) set `RETICORA_ENROLL_TOKEN` (the one-time code) and
`RETICORA_CLOUD_URL` (the server URL) instead (COL-04): without a keystore the
first start enrolls with a locally generated key and a CSR, stores the identity
(mode `600`) and starts normally; with a keystore it does not enroll again.
Only one of the two variables set, a damaged keystore or a refused code end the
start with exit code `1` and the cause in the log; the token is never logged
(details in `deploy/README.md`, "Enrollment"). The mTLS HTTP client is built
by `edgecore/transport.NewMTLS` (TLS 1.3 minimum). Without certificate
material the collector logs a warning and falls back to plain HTTPS/HTTP so
local development keeps working. Discovery results are uploaded as
gzip-compressed batches; when the backend is unreachable the batch is spooled
to the on-disk buffer (`RETICORA_SPOOL_DIR`, default
`/var/lib/reticora-collector/spool`, implemented by `edgecore/buffer`) and
flushed in oldest-first order once connectivity returns. The spool is bounded:
`RETICORA_SPOOL_MAX_BYTES` (default 1 GiB, `0` = unlimited) caps its total
size — oldest messages are dropped first — and `RETICORA_SPOOL_MAX_AGE`
(default 72h, `0s` = keep forever) expires stale messages on enqueue. The next
successful sync re-discovers the current state anyway, so dropping the oldest
data first is the safe degradation. Spool occupancy (message count, bytes,
oldest age) is emitted as structured `spool stats` log records on every
spool/flush so operators can alert on a growing backlog.

**Probe spool (PRB-07, NFR-04, COL-05, WP-954):** next to the discovery
spool the monitoring keeps two queues of its own below `RETICORA_SPOOL_DIR`:
`probe-state` (every state change as an entry of its own, written before
the engine goes on; kept at least 24 h and never displaced within them)
and `probe-results` (the results, bundled every second or at 1,000 results
into one gzip-compressed entry with `seq_from`/`seq_to`, delivered in
sequence order). `RETICORA_PROBE_SPOOL_MAX_BYTES` (default 2 GiB) is the
budget of both together, a tenth of it for the state changes; 24 h at 170
results/s fit into it. At 80 % of the result share the oldest bundles are
compacted (per probe one result per 5 minutes plus every failure and every
result of a state change): a window of up to 900 bundles behind a cursor,
the last compacted part included so it thins out again with the bundles
behind it; a full window with nothing to leave out (an outage with only
failures) is passed over, so the compaction never stalls on it and reads
only the bundles behind its cursor (`DiskBuffer.PeekAfter`). Only at the
limit a new bundle is dropped. Losses are
counted per reason (`max_bytes`, `compacted`, `quarantined`; compacted
results are no loss), persisted and reported in the monitoring alive
message (`collector/probe/outbox.go`).

**Identity resolution (spec §5.3):** `discovery.Reconcile` matches incoming
items against existing CIs in the spec's priority order — serial number,
hardware UUID, MAC address(es), management IP + CI type, hostname/FQDN. Each
ingest item carries a `source` (snmp/ssh/redfish/ipmi/wmi/…); conflict
resolution follows source trust plus recency: data from an equal or more
trusted source (IPMI/Redfish > API > agent > WMI/SSH > SNMP > sweep > manual)
always wins, while a less trusted source only overwrites values older than
seven days (`discovery.ShouldApplyAttribute`). A low-trust sighting still
refreshes `last_seen_at` and merges new fingerprint keys, but cannot clobber
fresh high-trust identity data. When a matched CI contradicts the incoming
identity values (e.g. a changed serial number), a `conflicting_values` review
item with both identity snapshots is queued at
`/api/v1/discovery/review-items` instead of silently overwriting; ambiguous
matches keep landing in the queue as `ambiguous_identity`.

**Classification (spec §5.4):** vendor profiles in `collector/profiles/data/`
are embedded into the collector binary (`profiles.Registry.LoadEmbedded`). The
SNMP plugin classifies devices by sysObjectID (longest-prefix match via
`Registry.BySysObjectID`) and fills vendor/model plus profile attributes from
the matched profile; `Registry.VendorByMAC` resolves vendors from MAC OUI
prefixes. Adding a new device family is a data-only change — ship a JSON
profile; `TestEmbeddedProfileData` validates the set in CI. Shipped profiles
cover Cisco, Juniper, Arista, Fortinet, HPE, Dell, MikroTik, Ubiquiti,
Synology, NetApp, APC, Supermicro and Lenovo plus generic SNMP/SSH fallbacks.

**SNMP trap reception:** when `RETICORA_SNMP_TRAP_LISTEN` is set (e.g.
`:162`), the collector runs a tolerant SNMPv1/v2c trap receiver
(`collector/plugins/snmp/trap.go`). Traps carrying a different community than
`RETICORA_SNMP_COMMUNITY` are dropped; every accepted trap is normalized into
a `snmp_trap_received` metric sample (labels: source IP, trap OID, up to 16
varbinds) and posted to `POST /api/v1/monitoring/metrics`, so the standard
alert rules can fire on traps. Trap delivery is fire-and-forget — a burst
during a backend outage is logged and dropped rather than spooled, because a
delayed alert is usually worse than a lost one. Trap storms are bounded by a
64-events-in-flight cap. The BER parser is deliberately tolerant: malformed
varbinds are skipped, never fatal.

**SNMPv3 polling:** the collector polls SNMPv3 only with authPriv
(`collector/plugins/snmp/v3.go`) through gosnmp v1.47.0, which fixes
GHSA-w96x-5v55-24xx (forged unauthenticated answers accepted before the
engine ID is known, remote panic in USM parsing). Since v1.47.0 the engine
discovery fails when the agent's REPORT carries an empty engine ID or one
outside 5–32 bytes (RFC 3411 `SnmpEngineID`); such a non-conforming agent
is reported as a failed target instead of being polled.

**Identity values per record (SR-10, WP-878):** a DeviceRecord
(`api/devicerecord.schema.json`) carries at most 256 identity values
(`x-max-identity-keys`; counted as distinct values: serial number, hardware
UUID, every MAC of identity and interfaces, every IP of the interfaces and
`target_ip`). The server refuses a record with more on its own and never
shortens it: HTTP ingest and edge channel count it in
`rejected_identity_limit` of the answer, log its `record_id` with the count and
raise `reticora_ingest_records_rejected_total{reason="identity_keys"}`; the
other records of the batch are processed. A record within the limit never
takes the organization's identity lock exclusively.

**Metric polling (Epic E, MON-02):** the collector polls base metrics over
SNMP and uploads them to the monitoring ingest (labels `collector_id` and
`kind` are added on upload):

- *Interface counters (standard poll, `kind=snmp_poll`):* on by default every
  300 s on every scan target (`RETICORA_SCAN_SUBNETS`), 16 targets at a time.
  `RETICORA_METRICS_INTERVAL` sets the interval (e.g. `1m`), `0` turns the
  poll off. Per interface — at most 256 per device, the first by ifIndex —
  `if_in_octets` and `if_out_octets` (64-bit `ifHCInOctets`/`ifHCOutOctets`
  of the ifXTable; without ifXTable the 32-bit ifTable counters) and
  `if_oper_status` (1 up, 2 down, …), labelled `target`, `ifIndex` and
  `ifName` when the device reports it. `RETICORA_SNMP_POLL_METRICS`
  (`name=oid,name=oid`, …) replaces the standard poll with one GET of the
  listed OIDs.
- *Power readings (`kind=power`):* after every discovery pass with the
  `power` protocol, per power device found, labelled `target` and
  `device=ups|pdu`. A UPS gives the UPS-MIB values `ups_battery_status`,
  `ups_battery_runtime_seconds`, `ups_battery_charge_percent` and
  `ups_output_load_percent`. A PDU gives `pdu_current_amps` and, where the
  device reports it, `pdu_active_power_watts` from the metering table of its
  vendor family, which the probe recognises by the outlet table: APC Rack
  PDU 2 (rPDU2PhaseStatusTable, current and power per `phase`, plus
  `module`), APC Rack PDU/MasterSwitch (rPDULoadStatusTable, current per
  `phase`; bank rows are left out) and Raritan PX2/PX3 (PDU2-MIB inlet
  sensors, current and active power per `inlet`). PDUs of other vendors give
  no readings.

Values a device does not answer (noSuchObject) or answers non-numerically are
skipped, an unreachable target is skipped for the cycle, so one failing
device never stalls it. SNMP load of the interface poll per device and
interval: one GETBULK walk (max-repetitions 50) per column — ifOperStatus,
ifName, ifHCInOctets, ifHCOutOctets — that is `ceil((n+1)/50)` requests per
column for n interfaces, at most 6 at the 256-interface cap: up to 24 GETBULK
requests of up to 50 varbinds each every 300 s (a device without ifXTable:
two more 32-bit counter walks, the ifXTable walks end after one request
each). A silent host costs one timeout (5 s) per cycle. The power poll adds
one GET of four OIDs per UPS or one walk of two to four short columns per
PDU per discovery pass; the probe asks every SNMP-answering host one GETNEXT
for the outlet tables.

**Alert evaluation (Epic E):** the server evaluates enabled alert rules once
per minute against the metric store (`monitoring.Evaluator`). A rule fires
only when its condition holds continuously for the configured duration; the
pending/fired state is persisted on `alert_rule` (migration 0037), so
restarts neither re-notify nor lose ongoing durations. Fired alerts are
logged as structured warnings via the default notifier.

**AI/RAG governance:** `/api/v1/ai/conversations` and `/api/v1/ai/ask` are
gated by the `ai` add-on entitlement (CH14). If no
OpenAI-compatible provider is configured, the handler returns HTTP 503 with a
problem document. Retrieval first asks the search backend for tenant-owned
candidates, applies the same permission checks, then ranks matching `ai_chunk`
rows with cosine similarity when embeddings are configured or lexical scoring
otherwise. Governance is enforced fail-closed and covered by tests
(`backend/internal/ai`): chunks from a foreign tenant are dropped even if a
misconfigured search backend returns them (defense in depth on top of RLS),
unknown entity types are denied, a failing permission check yields no chunks,
and `contact` chunks require `contact:read` like the search index. The system
prompt instructs the model to answer only from the supplied tenant context,
to state uncertainty and to cite the source of every statement. When no
tenant-owned chunks were retrieved, the answer is prefixed with an explicit
"no tenant data found" notice and returned without citations, so callers
surface the limitation instead of hallucinated sources. Every exchange is
recorded in `ai_conversation`/`ai_message` with token counts and citations so
answers remain auditable.

**Retrieval chunk pipeline:** `ai_chunk` is kept in sync with CI mutations by
`ai.NewCIChunkIndexer`, a second decorator on the CI repository next to the
search indexer, so every write path (REST, collector ingest, workflow) feeds
the assistant. Chunks store the CI title and summary as lexical content and
an embedding vector when `RETICORA_LLM_EMBEDDING_MODEL` is configured and
the organization opted in (`organization.ai_opt_in`) and holds the `ai`
add-on (AI-02); otherwise the provider is not called and the chunk stays
lexical. `RETICORA_AIR_GAPPED=true` disables every external AI call (chat and
embeddings); the start log reports the state. Embedding failures degrade gracefully to lexical scoring and never block the
CI write. Chunks for CIs created before the pipeline existed are backfilled
by `POST /api/v1/search/reindex`, which rebuilds the tenant's search index
(and thereby its chunks) from PostgreSQL as the source of truth.

**Binaries (`backend/cmd/`):**

- `server` — Main API server (HTTP + GraphQL BFF).
- `collector` — On-premise discovery collector.
- `agent` — Endpoint agent for server/client telemetry.

#### Event contract

Domain events (EVT-01 to EVT-03) follow `api/events.schema.json`. The register
is `backend/internal/events/types.go`: `events.Types` lists every event type in
the order of the schema's `type` enum, `events.Payloads` assigns every phase 2
type the Go struct of its data frame, and the `$defs` entry of the same name
describes it (`TestEnvelopeMatchesSchema`, `TestPayloadFramesMatchSchema`).
New types are added only in `types.go` and `api/events.schema.json`; the
producing work package adds its fields to the struct and the `$def` of its
type (both allow further fields).

- **Writing:** a mutation writes its events with `Outbox.Append` (or
  `AppendMany`) in its own transaction, as the last step after its audit
  entry; system jobs and workers use `events.PGEmitter`. `Append` refuses a
  type outside `events.Types`.
- **Phase 2 frame:** the data of a phase 2 type names its object (`id`; for
  `lifecycle.changed` `entity_type` and `entity_id`), carries `client_id` and
  `site_id` at the top level for objects with a client or site, and its core
  fields. Personal fields (`pii.IsPersonalKey`) carry a surrogate reference
  `{"$pii": "<id>"}` (WP-241). The envelope of a phase 2 type always names
  `entity_id` and `entity_seq`.
- **Phase 3:** the reservation of `maintenance.scheduled`, `workflow.*` and
  `finding.*` ended with WP-868; the types of phase 3 (`events.Phase3Types`)
  are registered with their frames and listed below.

| Type | `entity_id` | Origin | Producer |
|---|---|---|---|
| `ci.status_changed` | CI | EVT-03 | WP-707 |
| `lifecycle.changed` | CI or asset (`entity_type`) | EVT-03 | WP-724 (transition service, also in the disposal path of WP-278) |
| `relationship.changed` | relationship | EVT-03 | WP-296 |
| `asset.created`, `asset.changed`, `asset.deleted` | asset | EVT-03 | WP-740 |
| `asset.assigned`, `asset.returned` | asset | EVT-03 | WP-275 |
| `asset.warranty_expiring` | asset | EVT-03 (`asset.*`) | WP-270 |
| `contract.expiring` | contract | NTF-05 [P2] | WP-270 |
| `stock.movement` | moved object (asset or quantity item, not the movement row, so that `entity_seq` orders the events per object) | EVT-03 | WP-274 |
| `stock.below_minimum` | quantity item | NTF-05 [P2] | WP-285 |
| `reservation.created`, `reservation.released`, `reservation.expired`, `reservation.converted` | reservation | EVT-03 | WP-277, for quantity items WP-284 |
| `ticket.updated` | ticket | EVT-03 | WP-290 |
| `ticket.sla_breached` | ticket | plan addition | WP-292 |
| `metamodel.published` | publication (content after E-37) | EVT-03 | WP-261 |
| `document.scanned` | document | plan addition | WP-734 |
| `import.completed` | import run | plan addition | WP-775 |

Types of phase 3 (EVT-03 [P3]; monitoring of ALR-02, PRB-03 and ALR-07 with
WP-867, the modules with WP-868; E-161 Option 2): one event per state change, never one per probe result
(30.5 million results a day would exceed the outbox retention of EVT-01).
They are registered with their frame (`events.Phase3Types`); until its
producer is merged a type stands in `plannedProducers` of
`backend/internal/server/evt02_producers_integration_test.go`, never in
both lists, and the list is empty before WP-1177. Bound subscriptions read
the monitoring with `monitoring:read`, `ci.config_changed` too (not
`ci:read`), the module types with the read permission of their route
(`maintenance:read`, `workflow:read`, `finding:read`, `escalation:manage`);
`monitoring.suspension_changed` reaches org-wide subscriptions only.

| Type | `entity_id` | Origin | Producer |
|---|---|---|---|
| `probe.state_changed` | probe | ALR-02, PRB-05 | WP-961 |
| `alarm.opened`, `alarm.acknowledged`, `alarm.severity_changed`, `alarm.resolved` | alarm | ALR-01, ALR-02 | WP-982 |
| `alarm.escalated` | alarm | NTF-02 | WP-1146 |
| `incident.opened`, `incident.resolved` | incident | ALR-03 | WP-1028 |
| `incident.updated` | incident | ALR-03, ALR-10 | WP-1036 |
| `collector_group.unreachable`, `collector_group.reachable` | collector group | PRB-03 | WP-964 |
| `collector_group.failover` | collector group | PRB-10 | WP-963 |
| `collector.clock_skew` | collector | PRB-07 | WP-962 |
| `config.changed` | probe (configuration of the probe) | PRB-01 | WP-939 |
| `ci.config_changed` | CI (configuration of the device) | ALR-07 | WP-1054 |
| `monitoring.suspension_changed` | organization | PKG-06 | WP-942 |
| `capacity.forecast_exceeded` | forecast | ALR-08 | WP-1060 |
| `sla.violated` | availability target | ALR-06 | WP-1049 |
| `maintenance.scheduled` | maintenance window | MGT-04 | WP-1034 |
| `maintenance.started`, `maintenance.ended` | maintenance window | MGT-04 | WP-1033 |
| `workflow.run_started`, `workflow.run_succeeded`, `workflow.run_failed`, `workflow.run_cancelled` | workflow run | WFL-02 | WP-1092 |
| `workflow.approval_requested`, `workflow.approval_decided` | approval | WFL-04 | WP-1098 |
| `workflow.webhook` | workflow run | WFL-02 | WP-1096 |
| `finding.created`, `finding.changed`, `finding.resolved` | finding | SEC-02 | WP-1116 |
| `notification.escalated`, `notification.acknowledged` | notification escalation | NTF-02 | WP-1146 |
| `entitlement.changed` | organization (org-wide subscriptions only, `entitlement:read`); produced by the plan change | PKG-02 | WP-882 |
| `org.export_completed` | export (org-wide only, `org:export`) | TLC-05 | WP-1151 |
| `org.deletion_requested`, `org.deletion_cancelled` | deletion request (org-wide only, `org:export`) | TLC-06 | WP-1152 |

**`ci.status_changed`** (WP-707): an update of a CI
(`ci.PGRepository.Update`, e.g. `PATCH /api/v1/cis/{id}` and the bulk status
change) whose `changed_fields` contain `status` writes, in the same
transaction and right after `ci.changed`, the event `ci.status_changed`; its
`entity_seq` follows that of `ci.changed`. Its data are the CI snapshot of
`ci.changed` with `changed_fields`, plus `from` and `to` (old and new
status). The audit action stays as before (`ci.status_changed` for a status
change alone, else `ci.updated`). Behaviour change for webhooks: until
WP-707 a `ci.changed` with `changed_fields == [status]` also reached the
subscriptions of `ci.status_changed`, delivered under
`X-Reticora-Event`/`X-Webhook-Event: ci.changed`, and a status change
together with other fields did not reach them. Now these subscriptions
receive every status change exactly once as the event `ci.status_changed`,
under `X-Reticora-Event`/`X-Webhook-Event: ci.status_changed`, also when
further fields change at the same time; `ci.changed` no longer reaches them.
Other status changes raise no `ci.status_changed`: the offline detector
(`backend/internal/lifecycle/offline.go`) reports its changes
(`active` ↔ `unknown`) only as `ci.offline`/`ci.online`, deleting
(`decommissioned`) only as `ci.deleted`, restoring only as `ci.restored`.

Until its producer is merged a phase 2 type is registered without being
written; `TestEveryEVT02TypeHasAProducer` requires one producer case for every
type without a frame in `events.Payloads` and accepts one for a phase 2 type.

**Webhook subscriptions** (`webhook.ValidEvents`) derive from the register:
`events.Types` without `webhook.dead` and `apikey.creator_deactivated` (both
notify the org_admins only), the former names of `legacyNames` and the
`deprecatedEvents`. `webhook.test` cannot be subscribed to either. The OpenAPI
enum `WebhookEvent` lists the same names. The WebhooksPage offers new
subscriptions only the subscribable types of the register (WP-707); former
and deprecated names stay valid for existing subscriptions and are shown
unchanged in their list.

- **Former names** (`legacyNames`): `ci.updated` → `ci.changed`,
  `relationship.deleted` → `relationship.removed`, `lifecycle.transitioned` →
  `lifecycle.changed`, `inventory.movement` and `asset.movement` →
  `stock.movement`. A subscription on a former name receives the event of the
  new type once, under the new name. Since WP-274 the movement service writes
  `stock.movement` to the outbox (with `client_id` and `site_id` of the
  movement); `inventory.movement` is no longer dispatched directly.
  `reservation.created`/`reservation.released` are dispatched directly until
  WP-277.
- **Deprecated names without an event contract** (`deprecatedEvents`):
  `ci_type.created`, `ci_type.updated`, `ci_type.deactivated`,
  `ci_attribute.changed`, `composition.created`, `composition.updated`,
  `composition.deleted`, `override.changed` and
  `reconciliation.policy_changed` (dispatched directly by their handlers) and
  `discovery.completed` (never raised). They stay subscribable for existing
  subscriptions.
- **Bound subscriptions** (RBA-08): `webhook.EventReadPermission` assigns each
  type the read permission of its object's GET route — `asset.*` and
  `stock.movement` `asset:read`, `stock.below_minimum` `stock:read`,
  `contract.*` `contract:read`, `reservation.*` `reservation:read`,
  `ticket.*` `ticket:read`, `document.*` `document:read`, `metamodel.*`
  `ci:read`, `import.*` `import:run`. For `lifecycle.*` the payload's
  `entity_type` decides (`ci` → `ci:read`, `asset` → `asset:read`); without it
  bound subscriptions do not receive the event. The scope rules
  (`webhook/scope.go`) read `client_id`/`site_id` of the frame and resolve
  CIs, assets and quantity items by ID; metamodel, document and import events
  are organizational.

#### GraphQL-BFF

`POST /bff/graphql` (`backend/internal/graphqlbff`, GQL-01/GQL-02) is a
read-only GraphQL backend-for-frontend: it answers session principals only
(API keys and service tokens use the REST API), is not part of the public
REST contract, and every resolver checks its read permission and
entitlement and reads through the tenant-scoped repositories (RLS).

**API-only for now (GQL-01, F-332):** in phase 1 no page of the web UI uses
the BFF; every UI read goes through the REST API. The client
`bffQuery` (`frontend/src/api/client.ts`) is the prepared entry point and
stays covered by `client.test.ts`. The UI starts to use the BFF with its
first aggregate view (e.g. the CI detail with relationships and topology,
phase 2); until then the BFF serves API clients with a session.

**Limits (SEC-10, E-16 provisional):** depth 10, a complexity budget of
5,000 and a deadline of 10 s per request. A list costs its selection times
the size it may reach: `cis`, `searchEverything`, `reviewInbox` and `jobs`
their page (`first`, default 50, at most 100), `relationships` its page
(at most 100), `rackLayout` 100.
`topology` costs its selection per node it may hold (F-333): without a root
2,000 (`MaxTopologyNodes`), from a root the CIs a walk of `depth` hops
reaches at 10 edges per CI (the NFR-01 volumes: 100,000 relationships for
10,000 objects), i.e. 11 at depth 1, 111 at the default depth 2, 1,111 at
depth 3 and 2,000 from depth 4. A topology without a root therefore selects
about one field per node or edge (`{ topology { nodes { id } } }`); larger
selections need a root or the REST endpoint `GET /api/v1/topology`.

### Frontend Tech-Stack

The frontend is a **React 19 SPA** built with TypeScript and Vite.

**Core stack:**

| Technology | Version | Purpose |
|-----------|---------|---------|
| React | 19 | UI framework |
| TypeScript | 5.7 | Type safety |
| Vite | 6 | Build tool and dev server |
| React Router | 7 (package `react-router`, declarative mode, `useTransitions={false}`) | Client-side routing |
| TanStack Query | 5 | Server-state management, caching, background refetch |
| Zustand | 5 | Client-side state (lightweight, minimal boilerplate) |
| Radix UI | latest | Accessible, unstyled component primitives |
| Tailwind CSS | 3.4 | Utility-first styling |
| React Hook Form + Zod | 7 / 4 | Form handling with schema validation |
| i18next | 24 | Internationalization (German primary, English) |
| Lucide React | latest | Icon library |

**Project layout (`frontend/src/`):**

```
src/
├── api/
│   ├── client.ts       # Hand-written fetch wrappers (auth, refresh, errors)
│   └── generated/      # Generated from api/openapi.yaml — do not edit by hand
├── components/   # Reusable UI components (design system)
├── pages/        # Route-level page components
├── stores/       # Zustand stores (auth, UI state, preferences)
├── i18n/         # Translation resources and i18next config
├── main.tsx      # App entry point
└── App.tsx       # Router and provider setup
```

**Build & development:**

```bash
npm ci              # Install dependencies
npm run dev         # Start Vite dev server (HMR)
npm run build       # Production build (tsc + vite build)
npm run lint        # ESLint
npm run typecheck   # TypeScript type checking
npm run generate:api        # Regenerate the typed client from api/openapi.yaml
npm run generate:api:check  # Fail if the checked-in client is out of date
npm test                    # Vitest unit and component tests (also run in CI)
npm run e2e                 # Playwright end-to-end tests
```

**Routes:** `/dashboard`, `/cmdb` (CI list), `/cmdb/:id` (CI detail with
overview, attributes, relationships and topology neighbours), `/topology`,
`/racks`, `/discovery`, `/assets`, `/assignments`, `/documents`, `/stocktake`,
`/tickets`, `/users`, `/permissions`, `/slas`, `/forms`, `/workflows`, `/compliance`, `/iga`, `/assistant`, `/webhooks`, `/export` and `/monitoring`. Every CI is deep-linkable: list rows, topology nodes,
rack mounts and relationship entries all link to `/cmdb/:id`, so a CI can be
shared as a URL.

**UX conventions:** every data view distinguishes four states — loading
(`SkeletonList`, which reserves the layout and is announced via `role="status"`),
empty (`EmptyState`, which explains the situation and offers the next step),
error (`ErrorState`, announced via `role="alert"` and offering a retry) and
content. Canvas-based visualisations (topology graph, rack diagram) are always
accompanied by an equivalent list of focusable controls so the same information
is reachable by keyboard and screen reader. A skip link jumps to the main
content, and the command palette (`Ctrl`/`⌘`+`K`) reaches every page. All
strings live in `src/i18n/de.json` and `src/i18n/en.json`; both files carry the
identical key set.

**Generated API client:** `src/api/generated/schema.d.ts` is produced from
`api/openapi.yaml` by `openapi-typescript`; `src/api/generated/client.ts` wraps
it in a small `createApiClient` helper that derives the path, method, path
parameters, query parameters, request body and response type of every call from
the specification. Calling an undocumented path, using the wrong method for a
path or sending a body of the wrong shape is therefore a compile-time error, and
a specification change surfaces in the frontend as a type error instead of a
runtime 404. The generated files are committed so that the build does not depend
on generation order; CI runs `npm run generate:api:check`, which regenerates
them and fails on any diff. Regenerate with `make generate-api-client` (or
`npm run generate:api`) after every specification change.

**Browser-Unterstützung (UI-18, NFR-07):** Supported are the current and the
previous version of Chrome, Edge, Firefox and Safari, plus mobile Chrome and
Safari (E-88). The evidence is automated, without an external browser grid:

| Job | Versions | Projects | Artifacts |
|-----|----------|----------|-----------|
| `e2e` (`ci.yml`, every push and PR) | current: the builds of the `@playwright/test` in `frontend/package.json` | `chromium`, `edge` (installed Microsoft Edge), `firefox`, `webkit`, `mobile-chrome` (Pixel 7), `mobile-safari` (iPhone 14) | `playwright-report-<project>`, also when green, kept 30 days |
| `e2e-prev` (`e2e-prev.yml`, nightly and manual) | previous: `frontend/e2e-prev` pins the previous minor of `@playwright/test` (Chromium, Firefox, WebKit n-1); Chrome n-1 from Chrome for Testing | `chromium`, `chrome-prev`, `firefox`, `webkit`, `mobile-chrome`, `mobile-safari` | `e2e-prev-report-<project>`, kept 30 days |

Both jobs run the same specs of `frontend/e2e/` (smoke, the axe WCAG 2.1 AA
check `a11y.spec.ts`, keyboard and the core flows against the strict,
contract-checked mock backend); no test is skipped per browser. Both serve the
build with `vite preview` of `frontend/package-lock.json`
(`npm run preview -- --port 4173 --strictPort`, SPA fallback) and wait for it
with a curl retry; no server package is fetched by `npx` outside the lockfiles
(Upd-07: every `serve` ≥10.1.0 pins `compression` 1.8.1, GHSA-vc2v-76pw-4v95;
checked by `tests/ci-config.test.sh`, `workflow_npx_lockfile`). Limitations:
Edge n-1 is approximated by Chromium n-1 and Safari n-1 by WebKit n-1, since
no previous Edge or Safari build is available on the Linux runners; the
mobile projects are the desktop engines with a device profile (viewport,
touch, user agent), so `mobile-safari` is WebKit, not iOS Safari. A local run
of `e2e-prev`: `cd frontend/e2e-prev && npm ci && npx playwright install`,
serve a build with `npm run build && npm run preview -- --port 4173 --strictPort`
in `frontend/`, then
`CI=1 npx playwright test --config playwright.prev.config.ts --project=<name>`
(`chrome-prev` needs `PW_CHROME_PREV_EXECUTABLE`).

**Production serving:** The built SPA is served via Nginx (`nginx.conf`) in a Docker container with proper SPA fallback routing. For HTTPS, `install-cloud.sh` can provision a free Let's Encrypt certificate automatically (nginx + Certbot, ACME http-01 challenge): when a public domain is entered, it writes `RETICORA_TLS_DOMAIN` into `.env`, bootstraps a self-signed certificate, issues the real one via the `certbot` compose service and reloads nginx. The TLS configuration lives in `deploy/docker-compose/nginx-tls.conf.template` (rendered by the nginx entrypoint into `/etc/nginx/conf.d/reticora-tls.conf`, included by the image's `nginx.conf`); with TLS enabled the UI is served at `https://<domain>` on port 443 and port 80 handles the ACME challenge plus the HTTP→HTTPS redirect. Certificates are stored under `deploy/docker-compose/letsencrypt/` and renewed automatically.

### Deployment

Reticora is deployed on **Kubernetes** in an EU region with infrastructure managed by **Terraform**.

**Container images:**

- Backend server: Multi-stage Alpine build (`backend/Dockerfile`), runs as `nobody`.
- Frontend: Nginx-served static SPA (`frontend/Dockerfile`).

**Kubernetes (Kustomize):**

Manifests in `deploy/k8s/`:

- `base/` — the shared manifests: `deployment.yaml` (server with health probes
  and resource limits), `service.yaml`, `serviceaccount.yaml` (ServiceAccount +
  PodDisruptionBudget + secret-creation notes), `hpa.yaml` (autoscaling/v2,
  2–10 replicas on CPU) and the optional `components/opensearch` component.
- `overlays/staging` — single replica, no HPA, smaller resources,
  `RETICORA_ENVIRONMENT=staging`, namespace `reticora-staging`.
- `overlays/prod` — 3 replicas, HPA, higher resource envelope, includes the
  OpenSearch component and points telemetry at the OTLP collector.

`kustomize build` is run for the base and both overlays in CI
(`.github/workflows/ci.yml`, job `k8s-manifests`).

**Backup & disaster recovery:**

See `docs/backup-dr.md`. PostgreSQL PITR (base backup + WAL archive) is the
primary mechanism; `deploy/k8s/base/components/backup/cronjob.yaml` adds a nightly
logical `pg_dump` to S3 as a portable safety net (optional component
`deploy/k8s/base/components/backup`, included by the prod overlay; requires
the `reticora-backup` secret). The OpenSearch index is not backed up — it is
rebuilt online from PostgreSQL via
`POST /api/v1/search/reindex`. `.github/workflows/restore-test.yml` runs a
nightly restore test that dumps, restores into a fresh database, checks row
counts and verifies the audit hash chain with the `audit-verify` binary.

**Infrastructure services (docker-compose for local dev):**

| Service | Image | Purpose |
|---------|-------|---------|
| PostgreSQL + TimescaleDB | timescale/timescaledb:2.32.0-pg16@sha256:2f87efeaec6fbdd3ba4a6993b921c05d069cd8e980792cb23c7c5f32a6fe68a7 | Primary database with time-series |
| NATS | nats:2.14.10-alpine | Event bus (JetStream) |
| Redis | redis:7.4.12-alpine | Cache, sessions, rate-limiting |
| MinIO (Silo fork) | pgsty/silo:RELEASE.2026-11-12T00-00-00Z@sha256:c20d112390b9a9ce8c11b8af95ad35bcaa62f4759663c09c936cd8d2b511f329 | S3-compatible object storage; `minio/minio` is archived, the image name `pgsty/minio` is frozen since 2026-08-04 |
| Keycloak | keycloak:26.8.2 | OIDC identity provider |

**Terraform:**

`deploy/terraform/main.tf` provides the module scaffold for cloud provisioning (target region: `eu-central-1`).

**Grafana/Monitoring:**

Dashboards in `deploy/grafana/` (`dashboard-overview.json`, plus
`dashboard-tenant.json` for per-tenant rate/error/latency with an
`organization_id` template variable). SLO definitions and alert rules live in
`deploy/monitoring/slo-rules.yaml` (99.9 % availability, p95 read latency
< 500 ms), carrying the `organization_id` label when tenant metrics are
enabled.
Background workers export `reticora_worker_enabled`, `reticora_worker_up`,
`reticora_worker_runs_total`, `reticora_worker_errors_total`,
`reticora_worker_last_success_timestamp_seconds` and `reticora_worker_backlog`
per worker (OPS-05). The rules for stopped workers, errors, missing
successful runs and backlog are in `deploy/monitoring/worker-rules.yaml`.
`deploy/monitoring/alertmanager.yaml` routes all alerts to the operators;
see [`docs/ops/workers.md`](ops/workers.md).

**Tenant-aware observability (Epic H3):** the server exports
`reticora_http_requests_total` and `reticora_http_request_duration_seconds`
with `method`, routed `path`, `status` and `organization_id` labels on
`/metrics`. The `organization_id` label multiplies the series count by the
number of tenants, so it is opt-in via `RETICORA_METRICS_TENANT_LABEL=true`
(default off, all tenants aggregate into one series per method/path/status).
The routed path (e.g. `/api/v1/cis/{id}`) is used instead of the raw URL so
entity IDs never become label values. OpenTelemetry tracing/metrics export to
an OTLP HTTP collector when `RETICORA_OTEL_ENDPOINT` is set (with
`service.name` and `deployment.environment` resource attributes); with an
empty endpoint the providers stay no-op.

**Environment configuration:**

The server is configured via environment variables:
- `RETICORA_PORT` — HTTP listen port (default 8080).
- `RETICORA_DATABASE_URL` — PostgreSQL connection string of the runtime login
  (the installer creates `reticora_runtime`). The login itself must be
  `NOSUPERUSER NOBYPASSRLS` and must not reach a `SUPERUSER` or `BYPASSRLS`
  role, inherited or by `SET ROLE`; make it a member of `reticora_app` only.
  A `RESET ROLE` would otherwise undo the role switch below, so the server
  **refuses to start** with a role-contract error naming the session user
  (TEN-03, F-057). The migration/admin login (for example the bootstrap
  superuser) is for migrations, seed and backups only.
- `RETICORA_DB_APP_ROLE` — database role every pooled connection switches into
  (default `reticora_app`, created by migration `000056`). Row Level Security is
  bypassed for superusers, `BYPASSRLS` roles and — without `FORCE ROW LEVEL
  SECURITY` — table owners, so the server switches into a restricted role before
  serving any request and **refuses to start** if the effective role can still
  bypass RLS. Set this only when the login role is already restricted; setting it
  to an empty value disables the role switch but not the startup check.
- `RETICORA_NATS_URL` — NATS server URL. Required with `RETICORA_ENVIRONMENT=production`: the server refuses to start without it, because only the NATS ingest queue takes the identity locks that keep several instances from duplicating devices with a weak identity (F-282). Development and staging start without it (ingest in the request, without identity locks) and log a configuration warning. See [`docs/betrieb/ingest-spuren.md`](betrieb/ingest-spuren.md).
- `RETICORA_INGEST_LANES` — parallel ingest lanes per organization (default `4`, `1`–`16`, the same on every instance; `1` is the serial ingest of the releases before WP-334). See [`docs/betrieb/ingest-spuren.md`](betrieb/ingest-spuren.md).
- `RETICORA_PROBE_STREAM_MAX_BYTES` — byte limit of the JetStream stream `RETICORA_PROBE` that takes the collectors' probe state changes and results (default `5368709120`, 5 GiB). See [`docs/ops/workers.md`](ops/workers.md#probe-edge-and-stream-reticora_probe-wp-959).
- `RETICORA_PROBE_EDGE_WORKERS` — handlers per instance that answer the probe edge subjects in parallel (default and minimum `8`, at most `256`).
- `RETICORA_REDIS_URL` — Redis connection string.
- `RETICORA_ENVIRONMENT` — Environment name (development/staging/production). The collector reads the same variable: it accepts `RETICORA_SIMULATE=true` (simulated device fleet, SIM-02) only with `development` or `test` (case-insensitive) and exits with code 1 and `collector.simulate.refused` for any other value, including an unset or empty one (F-276).
- `RETICORA_DEFAULT_PLAN` — Plan provisioned for the `--no-db` demo organization (default `essential`); organizations in the database are provisioned with their own plan.
- `RETICORA_SIGNUP_VERIFY_URL` — page the self-signup verification link opens; the token is appended as `?token=` (TLC-01). Self-signup also needs `RETICORA_KEYCLOAK_ADMIN_CLIENT_ID` / `RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET` (a confidential client of the realm in `RETICORA_OIDC_ISSUER_URL` whose service account holds `realm-management/manage-users`) and the central SMTP server (`RETICORA_SMTP_*`); until all exist, `GET /api/v1/signup/challenge` and `POST /api/v1/signup` answer 503. The value must be an absolute http or https URL with a host and without user info, otherwise the start check fails; a doubled leading slash in the path is collapsed, so a base URL ending in `/` still gives `/onboarding`; development defaults to `http://localhost:5173/onboarding`. Docker Compose derives the page as `<RETICORA_PUBLIC_BASE_URL>/onboarding` (a value in `.env` wins) and forwards the admin client, the SMTP settings, the captcha key, the domain lists and the trial settings from `.env`; `install-cloud.sh` creates the admin client (`reticora-server-admin`) and the captcha key and asks for the SMTP server (see "Creating organizations" below).
- `RETICORA_KEYCLOAK_INTERNAL_URL` — optional base URL (`http(s)://host[:port]`) under which the server reaches Keycloak directly; the Keycloak admin client (signup, invitations, MFA binding) then sends its requests there with the public issuer host in `Host`/`X-Forwarded-*`, because the public reverse proxy does not expose `/admin/`. The compose stack sets `http://keycloak:8080`; empty means the issuer host.
- `RETICORA_INVITATION_ACCEPT_URL` — page the link of the invitation mail opens; the token is appended as `?token=` (TLC-03, NTF-05). An absolute `http` or `https` URL (`http` is accepted in production for the HTTP default path); development defaults to `http://localhost:5173/invitations/accept`, Docker Compose derives it from `RETICORA_PUBLIC_BASE_URL`. Without it `POST /api/v1/invitations` and `/invitations/{id}/renew` answer 503. The mail goes over the organization's e-mail channels only; webhook deliveries of `invitation.created` leave out the link.
- `RETICORA_MASTER_KEY_PREVIOUS` — master keys being rotated out (comma-separated, SEC-06): their data keys stay readable until the nightly `rotation.master_key` run has rewrapped them with `RETICORA_MASTER_KEY`; then remove them (see [`docs/ops/workers.md`](ops/workers.md)).
- `RETICORA_SMTP_HOST`, `RETICORA_SMTP_PORT` (587), `RETICORA_SMTP_USERNAME`, `RETICORA_SMTP_PASSWORD`, `RETICORA_SMTP_FROM`, `RETICORA_SMTP_TLS` (`starttls` default, `tls`, `none`; a user name needs `starttls` or `tls`, Go `net/smtp` sends no login unencrypted to a host other than `localhost`) — the central SMTP server of the installation (TEC-13): verification e-mails of the self-signup and the `central_smtp` notification channels, which send with the organization as sender name; DKIM and SPF are set up for the sender domain at the relay. Organizations of dedicated, on-premises and air-gapped installations configure their own server as an `smtp` channel; its credentials are stored encrypted (NTF-01).
- `RETICORA_SIGNUP_CAPTCHA_KEY` — HMAC key (at least 32 characters, the same on every instance) that signs the self-hosted ALTCHA challenges of `GET /api/v1/signup/challenge` (E-33); without it each instance draws a random key, so challenges verify only where they were issued. Signups additionally pass the pre-auth limit, at most 5 signups per client IP in any hour (sliding window in the shared cache, so it holds across replicas; Retry-After names the time until the oldest of them leaves the hour) and the disposable-domain blocklist (`backend/internal/signup/disposable/`, updated monthly by the *Disposable domains* workflow).
- `RETICORA_SIGNUP_BLOCKED_DOMAINS`, `RETICORA_SIGNUP_ALLOWED_DOMAINS` — comma-separated operator entries that extend the disposable-domain blocklist and allowlist; a domain covers its subdomains, the allowlist wins.
- `RETICORA_TRIAL_PLAN`, `RETICORA_TRIAL_DAYS`, `RETICORA_TRIAL_MAX_CIS` — trial of a self-signed-up organization (ENT-07, defaults `standard`, `30`, `100`); features the `essential` plan lacks expire at the trial's end; the daily system job `entitlement.trial_end` then switches the organization to the fallback plan with its default limits (`max_cis` 500, E-157, WP-884), unless an operator changed the plan before.
- `RETICORA_ENTITLEMENT_ENFORCEMENT` — Set to `false` to disable feature/limit enforcement (default `true`).
- `RETICORA_SEARCH_BACKEND` — `postgres` (default) or `opensearch`.
- `RETICORA_OPENSEARCH_URL`, `RETICORA_OPENSEARCH_USERNAME`, `RETICORA_OPENSEARCH_PASSWORD`, `RETICORA_OPENSEARCH_INDEX` — OpenSearch connection settings.
- `RETICORA_LLM_BASE_URL`, `RETICORA_LLM_API_KEY`, `RETICORA_LLM_CHAT_MODEL`, `RETICORA_LLM_EMBEDDING_MODEL` — OpenAI-compatible chat and embedding provider settings.
- `RETICORA_AIR_GAPPED` — `true` disables every external AI call (air-gapped profile, AI-02; default `false`).
- `RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED` — the CI offline detector, worker `offline_detector` (scheduler run `ci.offline_detector`, every 5 min; REC-09, LCY-05): an observable, discovered CI (not `is_manual`, source not manual/import) whose `last_seen_at` is older than three times the interval of its discovery scope becomes `unknown` with the event `ci.offline`, and `active` again with `ci.online` once seen; without a scope the organization's `settings.discovery.sweep_interval` (default 24 h) applies. The run changes a CI only as it read it (same status and `last_seen_at`, no other writer holding the row); a sighting the ingest writes during the run keeps the CI active, and a CI it set unknown before the sighting reached it is active again with `ci.online` (F-412). Each change is part of the CI's history and the audit log in the transaction of the change: a change set `status_changed` (source `system`, comment `ci.offline_detector`, the version stays) and the audit entry `ci.status_changed` (actor type `system`, `changes.job` `ci.offline_detector`; F-413). Default `true`. The former `RETICORA_OFFLINE_DETECTOR_ENABLED` still applies without it and is logged as deprecated (docs/ops/workers.md).
- `RETICORA_WORKER_COMPUTED_REFRESH_ENABLED` — the refresh of computed attributes, worker `computed_refresh` (scheduler run `attribute.computed_refresh`, every hour; JOB-02 [P2], WP-718): per organization, in tenant transactions of at most 100 CIs, it recomputes the computed values (MET-10) whose expression uses `today` (the date in `organization.timezone`) or an asset field, e.g. the system attribute `warranty_days_left` (days until `warranty_end` of the linked asset). Only changed values are written with their provenance (source `computed`, rank 100); `ci.version` stays, and there is no history, audit entry, `ci.changed` or outbox event. The backfill after a change of a computed definition (`attribute.computed_backfill`, every minute) runs without this switch. Default `true` (docs/ops/workers.md).
- `RETICORA_WORKER_PROBE_ASSIGN_ENABLED` — the probe assignment engine, worker `probe_assign` of the module `monitoring` (scheduler run `probe.assign`, every 10 s; PRB-03, PRB-11, WP-941): each collector group under the lock `probe_assign:<group>` assigns every enabled probe to exactly one healthy member (weighted rendezvous hashing in mode `distributed`, the member with the highest priority in mode `failover`); a changed assignment gets `epoch` + 1 (fencing token), counted from the high-water mark of the probe in `probe_epoch` (migration 000338), so an epoch never goes down and is never issued twice, also after the assignment row went (disable, group change, member left); the `config_version` of the former and the new collector rises by one. A run locks the probes of its plan `FOR SHARE` first (the order of the probe writes: probe row, then assignment, then member) and drops what a write changed meanwhile. Probe writes assign their groups at once after the commit. Default `true` (docs/ops/workers.md).
- `RETICORA_WORKER_PROBE_INGEST_ENABLED` — the probe result ingest, worker `probe_ingest` of the module `monitoring` (NFR-11, PRB-07, WP-960): the durable consumer `reticora-probe-results` of the stream `RETICORA_PROBE` writes up to 500 result batches per fetch, per organization in one tenant transaction, through `probe_result_v` (`unnest`, `ON CONFLICT DO NOTHING`: no duplicate rows), moves `probe_ingest_cursor` and records the credential use of the results (CRD-06); results of unknown probes and of a collector outside the organization or bound to another client than the probe are dropped and counted (`probe_ingest_discarded_total{reason="unknown_probe"|"foreign_collector"}`), results with another epoch or from another than the assigned collector are stored with `stale_epoch` and record no credential use (PRB-11); acknowledged only after the commit, `MaxDeliver` 5. Its backlog appears in `/readyz`. Default `true` (docs/ops/workers.md).
- `RETICORA_MODULES` — the late modules (phase tag P2 and later or add-on [A]; API-06, RBA-05) this release serves (WP-214, E-03). Empty (default) is the release G2 (WP-830), the same as the phase value `P2`: every module with phase P2 is on, the modules of P3, P4 and the add-ons [A] stay off (E-03, SEQ-01). A module that is off has no registered routes (404), its workers and system jobs do not start (the alert evaluator of `monitoring`; with `inventory` off also the reservation sweeper and the expiry run `expiry.check`, with `ticketing` off the SLA breach run `sla.check`) and the UI hides its navigation (`GET /api/v1/modules`). The default does not enter the start check of the malware scan: only an explicit `documents`, `P2`, `P3` or `all` makes the start outside development require `RETICORA_CLAMD_ADDR` and `RETICORA_NATS_URL`; without a clamd new document versions stay `pending_scan` (fail-closed). Release G1 (every late module off) is a test profile only (`server.NoModules`), no value of the variable selects it. `all` switches every module on; otherwise only the modules named in a comma list of the phase values `P2` (every module with phase P2, WP-706; combinable with module keys, e.g. `P2,gps`) and `P3` (every module with phase P2 or P3, release G3, WP-863; the empty default stays G2 until WP-1166) and the module keys `inventory`, `documents`, `import`, `metamodel`, `stocktake`, `ticketing`, `disposal` (P2), `maintenance`, `workflows`, `compliance`, `findings`, `monitoring`, `ipam`, `admp`, `escalation`, `org_lifecycle` (P3; the former key `alerting` still selects `monitoring` and is reported as a configuration warning), `keys`, `trainings`, `desks`, `gps`, `agents` (P4), `iga`, `ai` (add-ons, still gated by their entitlement). An unknown key stops the start. Their operations carry `x-phase` in `api/openapi.yaml`; the operations of P2 and P3 modules also carry the feature key of the org gate as `x-feature` (E-38, WP-863). A route belongs to the module with the longest matching prefix: `/api/v1/assets/{id}/locations` (GPS history, LOC-09) belongs to `gps`, not `inventory`. The P2 resources of E-38 without a module of their own follow E-105: the metamodel resources go to the module `metamodel` (attribute sets since WP-250, without feature key; the others add their prefix with their first route), suppliers, contracts and labels to `inventory`, business calendars and tickets from review items to `ticketing`; tags, the successors of G1 paths and the sub-paths of core resources are core without release gate (register in `backend/internal/server/modules_test.go`).
- `RETICORA_S3_ENDPOINT`, `RETICORA_S3_BUCKET`, `RETICORA_S3_ACCESS_KEY`, `RETICORA_S3_SECRET_KEY`, `RETICORA_S3_USE_SSL` — object storage for asynchronous export jobs (MinIO/S3).
- `RETICORA_S3_PUBLIC_URL` — base URL under which browsers reach the object store for signed downloads of exports and documents (`download_url`). The signature stays the one over `RETICORA_S3_ENDPOINT`; the URL names this base instead. The compose stack sets `<RETICORA_PUBLIC_BASE_URL>/objects`, which the frontend nginx forwards read-only (GET of the buckets `exports` and `reticora-documents`, `Host` of the internal endpoint) to MinIO (`frontend/objects-proxy.conf`). Empty keeps the internal endpoint in the URL, which a browser can only use if it resolves that name; from an HTTPS page an `http://` URL is blocked as mixed content (F-376). Other deployments point it at a reverse-proxy path that forwards the same way.
- `RETICORA_BLOB_DIR` — filesystem blob storage used by export jobs in `--no-db` development mode (defaults to a temp directory).
- `RETICORA_OTEL_ENDPOINT` — OTLP HTTP collector endpoint for traces/metrics; empty (default) keeps no-op telemetry.
- `RETICORA_METRICS_TENANT_LABEL` — set to `true` to add the `organization_id` label to HTTP request metrics (default `false`; multiplies series by tenant count).
- `RETICORA_PPROF_ADDR` — loopback address (e.g. `127.0.0.1:6060`) of a separate listener with the Go profiles of the server (`/debug/pprof/`, CPU profile `/debug/pprof/profile?seconds=N`; no command line), for load runs (`load.yml`, WP-438). Empty (default) starts no listener; the server refuses to start with a non-loopback address, outside `RETICORA_ENVIRONMENT=development` and together with `RETICORA_EGRESS_ALLOW_PRIVATE=true`. The API never serves the profiles.
- `RETICORA_EGRESS_ALLOW_PRIVATE` — set to `true` to let webhooks, IGA connectors and organizations' own SMTP servers reach private, loopback and link-local destinations, SMTP servers also on ports other than 25, 465 and 587 (dedicated, on-premises and air-gapped installations with an internal relay; default `false`). Cloud metadata endpoints stay blocked; DNS pinning and the 3-redirect limit always apply (SEC-08, [`docs/ops/workers.md`](ops/workers.md#destinations-of-the-notifier-sec-08)).
- `RETICORA_PREAUTH_RATE_LIMIT_RPM` — authentication attempts per client IP and minute before any credential check (default `20`, AUT-10): requests to `/auth/callback`, `/auth/refresh`, `/collectors/enroll`, `/signup`, `/signup/verify` and `/invitations/accept` and every failed authentication count (`/auth/refresh` only when it fails); once spent, the IP gets 429 until the minute is over. Concurrent requests with valid credentials do not spend it: a credential check holds a unit only until it succeeds, and a request that finds every unit held by checks in flight waits for one (at most 2.5 s) instead of getting 429 (F-387). Per server instance at most twice this budget of requests of one IP and 256 of all IPs wait; further requests get 429 at once. The budget belongs to fixed UTC-minute windows: checks admitted at the end of a minute keep their units, so right after the minute changes up to twice the budget can run at once, and two adjacent minutes together admit up to twice the budget of failed attempts.
- `RETICORA_RATE_LIMIT_RPM` — requests per minute per API key or session (default `600`, API-04).
- `RETICORA_RATE_LIMIT_ORG_RPM` — requests per minute of all credentials of an organization together (default `6000`, API-04; must not be below `RETICORA_RATE_LIMIT_RPM`). Responses carry `X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset`; a 429 adds `Retry-After`.
- `RETICORA_WORKER_<NAME>_ENABLED` — operator switch per background worker (OPS-02, default `true`): `WEBHOOK_DISPATCHER`, `EXPORT`, `INGEST`, `MONITORING`, `SCHEDULER`, `JOBS`, `EVENTS`, `NOTIFIER`, `PROBE_ASSIGN`, `PROBE_INGEST`. A disabled worker is not started in that process; see [`docs/ops/workers.md`](ops/workers.md).
- `RETICORA_TRUSTED_PROXIES` — comma-separated CIDRs of reverse proxies whose `X-Real-IP` header names the client for IP-based limits (default loopback and private networks; empty trusts none).
- Endpoint agent channel (AGT-03): the agent sends telemetry only over TLS — to the collector relay (`RETICORA_COLLECTOR_RELAY`, host:port) or directly to an `https://` `RETICORA_SERVER_URL`; there is no plaintext fallback. It authenticates with the `agent_token` returned once by `POST /api/v1/agents/enroll` (`RETICORA_AGENT_TOKEN` or `RETICORA_AGENT_TOKEN_FILE`); `RETICORA_TLS_CA_FILE` adds a private CA. The collector relay listens on `RETICORA_AGENT_RELAY_ADDR` (default `:9443`) only when `RETICORA_AGENT_RELAY_TLS_CERT_FILE` and `RETICORA_AGENT_RELAY_TLS_KEY_FILE` are set, and forwards each message with the agent's token to `/api/v1/agents/telemetry`.

### CI/CD

Continuous Integration runs on **GitHub Actions** (`.github/workflows/ci.yml`), triggered on push and pull requests to `main`.

**Backend job:**

1. Checkout code.
2. Setup Go 1.26.11.
3. `go mod download` — Fetch dependencies.
4. `go build ./...` — Compile all packages.
5. `go test -race -coverprofile=coverage.out ./...` — Run tests with race detector and coverage.
6. `go vet ./...` — Static analysis.

**Frontend job:**

1. Checkout code.
2. Setup Node.js 20 with npm cache.
3. `npm ci` — Install dependencies.
4. `npm run lint` — ESLint checks.
5. `npm run typecheck` — TypeScript type verification.
6. `npm test` — Vitest unit and component tests.
7. `npm run build` — Production build.

**Signed releases (`.github/workflows/release.yml`, TEC-15, COL-06):**

A version tag `v*` starts the release workflow. Tools follow E-31.

1. It builds the edge binaries `reticora-collector` and `reticora-agent` for linux/amd64 and linux/arm64.
2. It builds the images `reticora-server`, `reticora-collector`, `reticora-frontend`, `reticora-postgres` and `reticora-keycloak-db` and pushes them to `ghcr.io/datahub-chiemgau`.
   - `reticora-postgres` is TimescaleDB with pgBackRest (`deploy/docker-compose/postgres/Dockerfile`). Its base is pinned by version and digest (the same TimescaleDB reference as CI and the dev stack, E-96), its Go helpers `gosu` and `timescaledb-tune` are rebuilt with the repository's Go toolchain, and pgBackRest is built from its distribution tarball (release asset), checked by sha256.
   - `reticora-keycloak-db` is the PostgreSQL of Keycloak (`deploy/docker-compose/keycloak-db/Dockerfile`): the official `postgres:16.16-alpine`, pinned by version and digest, with its `gosu` rebuilt by the repository's Go toolchain (the vendor's is built with an outdated Go, E-96, A-85). Major version, data directory and UID 70 are the vendor's, so the `keycloakdb` volume of an earlier installation (`postgres:16.x-alpine`) keeps running.
   - Before the push, Trivy scans each image with the threshold of the CI job `image-scan`: Critical/High findings with an available fix block. Exceptions follow `security/scan-exceptions.txt`, see [security/abhaengigkeitsscans.md](security/abhaengigkeitsscans.md).
3. Syft generates an SPDX SBOM for every binary and every image, plus one for the source tree.
4. Everything is signed keyless with cosign v3.1.4 (E-258): a Fulcio certificate for the identity of the release workflow and an entry in the transparency log.
   - Binaries and SBOMs get a Sigstore bundle v0.3 (`<file>.sigstore.json`); a bundle in another format stops the release.
   - Images are signed, and their SBOM is attached as a cosign attestation (`spdxjson`); cosign v3 stores both as OCI referrers of the image digest.
   - Releases up to `v1.0.0-rc.1` were signed with cosign v2.4.1 (legacy bundle, signatures as `.sig` tags). The same verification commands accept them; `ci.yml` (job `cosign-verify`) proves this with the legacy bundle of `v1.0.0-rc.1`.
5. The workflow verifies its own signatures with the real cosign before anything is published (WP-545):
   - For linux/amd64 and linux/arm64, `edgecore/update/cosign_real_test.go` (build tag `cosignreal`) runs the updater's `CosignVerifier` against the collector and its bundle: the signed binary passes with the pinned release identity; the binary changed by one byte, the bundle of another artifact and another identity are refused. A failing or skipped test stops the release.
   - Every bundle passes `cosign verify-blob`; every image passes `cosign verify` (the signature names the image with the pushed digest, as the installers require) and `cosign verify-attestation --type spdxjson`. The release is published only after all five images passed.
   - It then publishes the files on the GitHub release, together with `SHA256SUMS` and one updater manifest per binary (`<binary>.release.json`, holding `downloadUrl`, `signatureBundleUrl` and `checksums.sha256`).

Release candidate of gate G1 (WP-595, `docs/security/review-g1.md`, section „Läufe auf dem Release-Kandidaten“): commit `536e3ab8` on `main` (gate decision of 2026-11-10, result „bedingt bestanden“; CI run 42712217817 and install smoke run 42712217841 green); tag `v1.0.0-rc.1` on `9989d9d3`, release run 42780580858 green (attempt 2), install smoke with release mode 42780580723 green: pre-release with SBOMs, Sigstore bundles, image signatures, SBOM attestations and the `cosign_real` test.

Mandatory signature check in the updater (`edgecore/update`):

- `Checker.Apply` installs an artifact only after its signature has been verified.
- The updater refuses, and leaves the running binary untouched, in these cases:
  - the release has no `signatureBundleUrl`;
  - the bundle cannot be loaded;
  - verification fails;
  - no verifier is configured.
- `CosignVerifier` calls `cosign verify-blob --bundle`. It pins the signer to `release.yml` of this repository on a `v*` tag, with the GitHub Actions OIDC issuer.
- It runs cosign only when `cosign version` reports at least v3.1.3 (`update.MinCosignVersion`, the same as `COSIGN_MIN_VERSION` of the installers, E-258, GHSA-fx35-mq7g-6g98); otherwise every update is refused and the collector warns at start (`collector.update.cosign_outdated`). Collector hosts with the cosign v2.4.1 of earlier installations: [`docs/betrieb/collector-updates.md`](betrieb/collector-updates.md), "cosign v3".

Check a release by hand (operators, before an installation or an air-gapped transfer) with cosign v3.1.3 or newer. Every command must exit 0 (`verify-blob` prints `Verified OK`, `verify` the verified signature payload); any other result means the file or image is not the one the release workflow signed. The signer is the release workflow of this repository on a version tag:

```bash
VERSION=v1.2.3   # the release tag
IDENTITY='^https://github\.com/DataHub-Chiemgau/Reticora-CMDB/\.github/workflows/release\.yml@refs/tags/v.+$'
ISSUER=https://token.actions.githubusercontent.com
```

Binaries, their SBOMs and the release manifest (files of the GitHub release, each with its `<file>.sigstore.json`), here for `reticora-collector` and `reticora-agent` on both architectures:

```bash
for file in reticora-collector-linux-amd64 reticora-collector-linux-arm64 \
    reticora-agent-linux-amd64 reticora-agent-linux-arm64 SHA256SUMS; do
  cosign verify-blob --bundle "$file.sigstore.json" \
    --certificate-identity-regexp "$IDENTITY" \
    --certificate-oidc-issuer "$ISSUER" \
    "$file"
done
sha256sum -c --ignore-missing SHA256SUMS
```

The same `verify-blob` call checks an SBOM (`<binary>.spdx.json`, `reticora-source.spdx.json`) and the source tarball (`reticora-source-<tag>.tar.gz`).

Images: the signature (`cosign verify`) and the SPDX SBOM attestation (`cosign verify-attestation --type spdxjson`) of all five images `reticora-server`, `reticora-collector`, `reticora-frontend`, `reticora-postgres` and `reticora-keycloak-db`:

```bash
for image in reticora-server reticora-collector reticora-frontend reticora-postgres reticora-keycloak-db; do
  cosign verify "ghcr.io/datahub-chiemgau/$image:$VERSION" \
    --certificate-identity-regexp "$IDENTITY" \
    --certificate-oidc-issuer "$ISSUER"
  cosign verify-attestation --type spdxjson "ghcr.io/datahub-chiemgau/$image:$VERSION" \
    --certificate-identity-regexp "$IDENTITY" \
    --certificate-oidc-issuer "$ISSUER" > /dev/null
done
```

`cosign verify` prints the image digest (`docker-manifest-digest`) and the signed reference (`docker-reference`: the repository for releases signed with cosign v2, `<image>@<digest>` since cosign v3); pin that digest (`ghcr.io/datahub-chiemgau/<image>@sha256:…`) instead of the tag, as the installers do in release mode. Without `> /dev/null`, `verify-attestation` prints the attestation; the SBOM is the `predicate` of the in-toto statement in its base64 `payload`.

**Planned additions:**

- Kubernetes deployment via GitOps (ArgoCD or Flux).
- Database migration validation in CI.
- Integration tests against docker-compose services.
- Static code analysis (CodeQL); dependency and container scans run in CI (`govulncheck`, `npm-audit`, `image-scan`).
- Dependency update automation (Dependabot/Renovate).

### Getting Started

See the root [README.md](../README.md) for setup instructions. When an
installation fails or misbehaves, see
[troubleshooting-installation.md](troubleshooting-installation.md) for the
known failure modes (container health, Keycloak/login, TLS, idempotent
re-runs) and their fixes.

### Creating organizations (tenants)

`install-cloud.sh` creates one organization, the default organization with
the initial administrator (`RETICORA_INITIAL_ADMIN_EMAIL`, org_admin of that
organization, not a platform operator). Further organizations come from two
places:

- **Self-signup (TLC-01):** `<RETICORA_PUBLIC_BASE_URL>/onboarding` ("Create
  account" on the login page). The visitor enters organization, e-mail
  address and name (the browser solves a proof-of-work challenge); the server
  sends a verification e-mail whose link (`/onboarding?token=…`), after the
  password is set, creates the organization (trial
  plan `RETICORA_TRIAL_PLAN`, `RETICORA_TRIAL_DAYS`, `RETICORA_TRIAL_MAX_CIS`)
  and its first org_admin with the chosen password (realm policy: 12
  characters, upper and lower case, digit, special character) and OTP at the
  first login. Open the page logged out or in a private window and the
  verification link in a new tab; a signed-in session or its Keycloak SSO
  session hides the steps. The e-mail verification is mandatory, so
  self-signup needs the central SMTP server: without `RETICORA_SMTP_HOST`
  the server answers `GET /api/v1/signup/challenge` with 503 ("self-signup is
  not available") and the installer's summary says
  `Self-signup disabled: no SMTP configured`. Keycloak's own registration
  stays off; it would create accounts without an organization.
- **Operators (SEC-07):** `POST /api/v1/admin/orgs` with an operator session
  of a member of the Keycloak group `operators` (OTP; login flow in
  [deploy/keycloak/README.md](../deploy/keycloak/README.md), "Operators").
  The installer's administrator is not an operator; add an account to the
  group first. With `RETICORA_SMTP_HOST` the first administrator of the
  organization gets an invitation e-mail from Keycloak over the realm's SMTP
  server (`admin_invited: true`). Without it the organization is created
  without an IdP account (`admin_invited: false`): create the account in
  Keycloak by hand (same e-mail address, user attribute `organization_id` =
  the `id` of the response, realm role `org_admin`, group `reticora-admin`;
  [deploy/keycloak/README.md](../deploy/keycloak/README.md), "Manual
  import", step 5); it is linked at the first login. There is no UI for it.

**Outgoing e-mail (SMTP):** the installer asks for the relay at "Outgoing
e-mail (SMTP)" — host, TLS mode (`starttls` on 587, `tls` on 465, `none`),
port, user name and password (optional, not asked with `none`), sender (`address` or
`Name <address>`, checked like the server's Go `net/mail`: no `,` `;` `:`
`@` `[` `]` `(` `)` `<` `>` in the name) — or takes `RETICORA_SMTP_HOST`,
`RETICORA_SMTP_PORT`, `RETICORA_SMTP_TLS`, `RETICORA_SMTP_USERNAME`,
`RETICORA_SMTP_PASSWORD` and `RETICORA_SMTP_FROM` from the environment,
otherwise from `.env`, in a non-interactive run; an empty host keeps e-mail
off. Without `RETICORA_SMTP_PORT` the port follows the TLS mode (465 for
`tls`, 587 otherwise) unless `.env` holds a port other than the previous
mode's default. It checks the relay
over TCP before the start (with a host, `/readyz` depends on it), writes the
settings to `.env` (mode `0600`), from where Docker Compose forwards them to
the server, and sets the same server for the Keycloak realm. The relay
sends the verification mails of the self-signup, invitations and the
notification mails of the `central_smtp` channels; set up SPF/DKIM for the
sender domain there. See
[troubleshooting-installation.md](troubleshooting-installation.md),
"Self-signup answers 503".

### Installer hardening (Epic A)

The installers validate their own work instead of failing later with opaque
errors:

- `configure_realm` validates the rendered Keycloak realm file
  (`validate_realm_json`: no leftover placeholders, parseable JSON, correct
  realm) before the container ever sees it.
- After Keycloak starts, `validate_keycloak_bootstrap` probes the realm's
  OIDC discovery document and warns when a localhost issuer is combined with
  a non-localhost public URL (the browser resolves the issuer, not the
  server).
- `validate_tls_material` checks every generated/issued certificate: parses,
  carries a subjectAltName, is unexpired, and matches the private key.
- `tests/install-cloud-helpers.test.sh` unit-tests these helpers without
  external dependencies; `.github/workflows/install-smoke.yml` runs the full
  installer in CI and asserts that every container becomes healthy.
