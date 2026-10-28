# Reticora CMDB Documentation

## Architecture

Reticora CMDB is a multi-tenant Configuration Management Database designed for Managed Service Providers (MSPs) and Internet Service Providers (ISPs).

### Technology Stack

- **Backend**: Go 1.26.10+, chi v5 router, pgx v5 (hand-written SQL)
- **Frontend**: React 18, TypeScript, Vite, TanStack Query v5, Zustand, React Router v6
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
`feature-not-entitled`; GraphQL fields check their feature as well. Quotas are
enforced in the domain service (`ci.Service.Create`) and answer with 403
`entitlement-limit`. After `valid_until` only discovery stops (CH21): ingest
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
E-12). Only the feature keys of ENT-02, the module keys and the add-ons and
the quotas `max_cis`, `max_collectors`, `max_users`, `max_api_keys` are
accepted; any other key is 422 with the field.
Attribute rules use the JSON rule AST of `api/rules.schema.json` (MET-45,
phase 1: required, pattern, range, enum, combined with all).
`backend/internal/rules` evaluates them bindingly, `frontend/src/rules` for
form previews; both run the cases of `api/rules.conformance.json`.

Users join an organization by invitation (`/api/v1/invitations`, TLC-03,
`user:manage`): an invitation names the address, a role and its scope
(org, client or site within the issuer's own scope), is valid for seven
days and stores only the hash of its token, which the response shows once
and the invitee receives by mail as link (`invitation.created`, NTF-05).
`POST /api/v1/invitations/accept` (public, pre-auth limit) consumes it
atomically and once and creates the user with that role; with the Keycloak
admin client the accepting person's password creates the IdP account.
Expired invitations are renewed with a new token; pending ones can be
revoked. Every step is audited.
Operators create organizations with plan and limits through
`POST /api/v1/admin/orgs` (TLC-02): the same creation chain as the
self-signup (slug, default VRF, first org_admin, entitlements of the plan
with the given limits), atomically with an `org.create` entry in
`operator_audit`; with the Keycloak admin client the first org_admin is
invited by the IdP's mail to set a password.
Plan matrix (ENT-06):

| Plan | Features |
|------|----------|
| essential | cmdb_core, discovery, topology, rack_view, export_csv, webhooks, api_access, notifications_email, inventory |
| standard | + documents, stocktake, ticketing |
| pro | + monitoring, workflow_forms |
| enterprise | + endpoint_agent, compliance |

IGA (`iga`) and AI (`ai`) are add-ons (CH14): no plan includes them, only
their own entitlement row unlocks them. The proposed matrix and quotas of
ENT-06 (V) have no gate effect until confirmed (E-34): a quota only gates
when it is stored in the limits of a row.


**Permissions and SLA:** migration 0026 turns permissions into data instead of
leaving them as ad-hoc JSON strings. `permission` is the global catalogue,
`role_permission` grants catalogue entries to tenant roles, and the effective
permissions endpoint also folds in existing custom-role JSON grants so old data
continues to work while new code can validate against the catalogue. SLA policy
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
from the implementation in either direction. `/metrics` is the only deliberate
exception: it is the Prometheus scrape endpoint, not part of the tenant API.

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
and tenant-scoped. See `docs/opensearch.md` for the reindex runbook and
troubleshooting (yellow status, disk watermark, unreachable backend).

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
| React Router | 6 | Client-side routing |
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
contract-checked mock backend); no test is skipped per browser. Limitations:
Edge n-1 is approximated by Chromium n-1 and Safari n-1 by WebKit n-1, since
no previous Edge or Safari build is available on the Linux runners; the
mobile projects are the desktop engines with a device profile (viewport,
touch, user agent), so `mobile-safari` is WebKit, not iOS Safari. A local run
of `e2e-prev`: `cd frontend/e2e-prev && npm ci && npx playwright install`,
serve a build on port 4173, then
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
| PostgreSQL + TimescaleDB | timescale/timescaledb:2.31.0-pg16@sha256:322ae364eca3321cf2a48f1ef31213b87843413b3cf8feeebf54e999a26bb731 | Primary database with time-series |
| NATS | nats:2.10-alpine | Event bus (JetStream) |
| Redis | redis:7-alpine | Cache, sessions, rate-limiting |
| MinIO (Silo build) | pgsty/minio:RELEASE.2026-08-04T00-00-00Z | S3-compatible object storage; `minio/minio` is no longer published |
| Keycloak | keycloak:24.0 | OIDC identity provider |

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
- `RETICORA_REDIS_URL` — Redis connection string.
- `RETICORA_ENVIRONMENT` — Environment name (development/staging/production). The collector reads the same variable: it accepts `RETICORA_SIMULATE=true` (simulated device fleet, SIM-02) only with `development` or `test` (case-insensitive) and exits with code 1 and `collector.simulate.refused` for any other value, including an unset or empty one (F-276).
- `RETICORA_DEFAULT_PLAN` — Plan provisioned for the `--no-db` demo organization (default `essential`); organizations in the database are provisioned with their own plan.
- `RETICORA_SIGNUP_VERIFY_URL` — page the self-signup verification link opens; the token is appended as `?token=` (TLC-01). Self-signup also needs `RETICORA_KEYCLOAK_ADMIN_CLIENT_ID` / `RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET` (a confidential client of the realm in `RETICORA_OIDC_ISSUER_URL` whose service account holds `realm-management/manage-users`) and the central SMTP server (`RETICORA_SMTP_*`); until all exist, `POST /api/v1/signup` answers 503.
- `RETICORA_KEYCLOAK_INTERNAL_URL` — optional base URL (`http(s)://host[:port]`) under which the server reaches Keycloak directly; the Keycloak admin client (signup, invitations, MFA binding) then sends its requests there with the public issuer host in `Host`/`X-Forwarded-*`, because the public reverse proxy does not expose `/admin/`. The compose stack sets `http://keycloak:8080`; empty means the issuer host.
- `RETICORA_INVITATION_ACCEPT_URL` — page the link of the invitation mail opens; the token is appended as `?token=` (TLC-03, NTF-05). An absolute `http` or `https` URL (`http` is accepted in production for the HTTP default path); development defaults to `http://localhost:5173/invitations/accept`, Docker Compose derives it from `RETICORA_PUBLIC_BASE_URL`. Without it `POST /api/v1/invitations` and `/invitations/{id}/renew` answer 503. The mail goes over the organization's e-mail channels only; webhook deliveries of `invitation.created` leave out the link.
- `RETICORA_MASTER_KEY_PREVIOUS` — master keys being rotated out (comma-separated, SEC-06): their data keys stay readable until the nightly `rotation.master_key` run has rewrapped them with `RETICORA_MASTER_KEY`; then remove them (see [`docs/ops/workers.md`](ops/workers.md)).
- `RETICORA_SMTP_HOST`, `RETICORA_SMTP_PORT` (587), `RETICORA_SMTP_USERNAME`, `RETICORA_SMTP_PASSWORD`, `RETICORA_SMTP_FROM`, `RETICORA_SMTP_TLS` (`starttls` default, `tls`, `none`) — the central SMTP server of the installation (TEC-13): verification e-mails of the self-signup and the `central_smtp` notification channels, which send with the organization as sender name; DKIM and SPF are set up for the sender domain at the relay. Organizations of dedicated, on-premises and air-gapped installations configure their own server as an `smtp` channel; its credentials are stored encrypted (NTF-01).
- `RETICORA_SIGNUP_CAPTCHA_KEY` — HMAC key (at least 32 characters, the same on every instance) that signs the self-hosted ALTCHA challenges of `GET /api/v1/signup/challenge` (E-33); without it each instance draws a random key, so challenges verify only where they were issued. Signups additionally pass the pre-auth limit, at most 5 signups per client IP in any hour (sliding window in the shared cache, so it holds across replicas; Retry-After names the time until the oldest of them leaves the hour) and the disposable-domain blocklist (`backend/internal/signup/disposable/`, updated monthly by the *Disposable domains* workflow).
- `RETICORA_SIGNUP_BLOCKED_DOMAINS`, `RETICORA_SIGNUP_ALLOWED_DOMAINS` — comma-separated operator entries that extend the disposable-domain blocklist and allowlist; a domain covers its subdomains, the allowlist wins.
- `RETICORA_TRIAL_PLAN`, `RETICORA_TRIAL_DAYS`, `RETICORA_TRIAL_MAX_CIS` — trial of a self-signed-up organization (ENT-07, defaults `standard`, `30`, `100`); features the `essential` plan lacks expire at the trial's end.
- `RETICORA_ENTITLEMENT_ENFORCEMENT` — Set to `false` to disable feature/limit enforcement (default `true`).
- `RETICORA_SEARCH_BACKEND` — `postgres` (default) or `opensearch`.
- `RETICORA_OPENSEARCH_URL`, `RETICORA_OPENSEARCH_USERNAME`, `RETICORA_OPENSEARCH_PASSWORD`, `RETICORA_OPENSEARCH_INDEX` — OpenSearch connection settings.
- `RETICORA_LLM_BASE_URL`, `RETICORA_LLM_API_KEY`, `RETICORA_LLM_CHAT_MODEL`, `RETICORA_LLM_EMBEDDING_MODEL` — OpenAI-compatible chat and embedding provider settings.
- `RETICORA_AIR_GAPPED` — `true` disables every external AI call (air-gapped profile, AI-02; default `false`).
- `RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED` — the CI offline detector, worker `offline_detector` (scheduler run `ci.offline_detector`, every 5 min; REC-09, LCY-05): an observable, discovered CI (not `is_manual`, source not manual/import) whose `last_seen_at` is older than three times the interval of its discovery scope becomes `unknown` with the event `ci.offline`, and `active` again with `ci.online` once seen; without a scope the organization's `settings.discovery.sweep_interval` (default 24 h) applies. Default `true`. The former `RETICORA_OFFLINE_DETECTOR_ENABLED` still applies without it and is logged as deprecated (docs/ops/workers.md).
- `RETICORA_MODULES` — the late modules (phase tag P2 and later or add-on [A]; API-06, RBA-05) this release serves (WP-214, E-03). Empty (default) switches them all off: their routes are not registered (404), their workers and system jobs do not start (alert evaluator, reservation sweeper) and the UI hides their navigation (`GET /api/v1/modules`). `all` switches every module on; otherwise a comma list of `inventory`, `documents`, `stocktake`, `ticketing` (P2), `maintenance`, `workflows`, `compliance`, `findings`, `alerting` (P3), `keys`, `trainings`, `desks`, `disposal`, `agents` (P4), `iga`, `ai` (add-ons, still gated by their entitlement). An unknown key stops the start. Their operations carry `x-phase` in `api/openapi.yaml`.
- `RETICORA_S3_ENDPOINT`, `RETICORA_S3_BUCKET`, `RETICORA_S3_ACCESS_KEY`, `RETICORA_S3_SECRET_KEY`, `RETICORA_S3_USE_SSL` — object storage for asynchronous export jobs (MinIO/S3).
- `RETICORA_S3_PUBLIC_URL` — base URL under which browsers reach the object store for signed downloads of exports and documents (`download_url`). The signature stays the one over `RETICORA_S3_ENDPOINT`; the URL names this base instead. The compose stack sets `<RETICORA_PUBLIC_BASE_URL>/objects`, which the frontend nginx forwards read-only (GET of the buckets `exports` and `reticora-documents`, `Host` of the internal endpoint) to MinIO (`frontend/objects-proxy.conf`). Empty keeps the internal endpoint in the URL, which a browser can only use if it resolves that name; from an HTTPS page an `http://` URL is blocked as mixed content (F-376). Other deployments point it at a reverse-proxy path that forwards the same way.
- `RETICORA_BLOB_DIR` — filesystem blob storage used by export jobs in `--no-db` development mode (defaults to a temp directory).
- `RETICORA_OTEL_ENDPOINT` — OTLP HTTP collector endpoint for traces/metrics; empty (default) keeps no-op telemetry.
- `RETICORA_METRICS_TENANT_LABEL` — set to `true` to add the `organization_id` label to HTTP request metrics (default `false`; multiplies series by tenant count).
- `RETICORA_PPROF_ADDR` — loopback address (e.g. `127.0.0.1:6060`) of a separate listener with the Go profiles of the server (`/debug/pprof/`, CPU profile `/debug/pprof/profile?seconds=N`; no command line), for load runs (`load.yml`, WP-438). Empty (default) starts no listener; the server refuses to start with a non-loopback address, outside `RETICORA_ENVIRONMENT=development` and together with `RETICORA_EGRESS_ALLOW_PRIVATE=true`. The API never serves the profiles.
- `RETICORA_EGRESS_ALLOW_PRIVATE` — set to `true` to let webhooks, IGA connectors and organizations' own SMTP servers reach private, loopback and link-local destinations, SMTP servers also on ports other than 25, 465 and 587 (dedicated, on-premises and air-gapped installations with an internal relay; default `false`). Cloud metadata endpoints stay blocked; DNS pinning and the 3-redirect limit always apply (SEC-08, [`docs/ops/workers.md`](ops/workers.md#destinations-of-the-notifier-sec-08)).
- `RETICORA_PREAUTH_RATE_LIMIT_RPM` — authentication attempts per client IP and minute before any credential check (default `20`, AUT-10): requests to `/auth/callback`, `/auth/refresh`, `/collectors/enroll`, `/signup`, `/signup/verify` and `/invitations/accept` and every failed authentication count; once spent, the IP gets 429 until the minute is over.
- `RETICORA_RATE_LIMIT_RPM` — requests per minute per API key or session (default `600`, API-04).
- `RETICORA_RATE_LIMIT_ORG_RPM` — requests per minute of all credentials of an organization together (default `6000`, API-04; must not be below `RETICORA_RATE_LIMIT_RPM`). Responses carry `X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset`; a 429 adds `Retry-After`.
- `RETICORA_WORKER_<NAME>_ENABLED` — operator switch per background worker (OPS-02, default `true`): `WEBHOOK_DISPATCHER`, `EXPORT`, `INGEST`, `MONITORING`, `SCHEDULER`, `JOBS`, `EVENTS`, `NOTIFIER`. A disabled worker is not started in that process; see [`docs/ops/workers.md`](ops/workers.md).
- `RETICORA_TRUSTED_PROXIES` — comma-separated CIDRs of reverse proxies whose `X-Real-IP` header names the client for IP-based limits (default loopback and private networks; empty trusts none).
- Endpoint agent channel (AGT-03): the agent sends telemetry only over TLS — to the collector relay (`RETICORA_COLLECTOR_RELAY`, host:port) or directly to an `https://` `RETICORA_SERVER_URL`; there is no plaintext fallback. It authenticates with the `agent_token` returned once by `POST /api/v1/agents/enroll` (`RETICORA_AGENT_TOKEN` or `RETICORA_AGENT_TOKEN_FILE`); `RETICORA_TLS_CA_FILE` adds a private CA. The collector relay listens on `RETICORA_AGENT_RELAY_ADDR` (default `:9443`) only when `RETICORA_AGENT_RELAY_TLS_CERT_FILE` and `RETICORA_AGENT_RELAY_TLS_KEY_FILE` are set, and forwards each message with the agent's token to `/api/v1/agents/telemetry`.

### CI/CD

Continuous Integration runs on **GitHub Actions** (`.github/workflows/ci.yml`), triggered on push and pull requests to `main`.

**Backend job:**

1. Checkout code.
2. Setup Go 1.26.10.
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
2. It builds the images `reticora-server`, `reticora-collector`, `reticora-frontend` and `reticora-postgres` and pushes them to `ghcr.io/datahub-chiemgau`.
   - `reticora-postgres` is TimescaleDB with pgBackRest (`deploy/docker-compose/postgres/Dockerfile`). Its base is pinned by version and digest (the same TimescaleDB reference as CI and the dev stack, E-96), its Go helpers `gosu` and `timescaledb-tune` are rebuilt with the repository's Go toolchain, and the pgBackRest tarball is checked by sha256.
   - Before the push, Trivy scans each image with the threshold of the CI job `image-scan`: Critical/High findings with an available fix block. Exceptions follow `security/scan-exceptions.txt`, see [security/abhaengigkeitsscans.md](security/abhaengigkeitsscans.md).
3. Syft generates an SPDX SBOM for every binary and every image, plus one for the source tree.
4. Everything is signed keyless with cosign: a Fulcio certificate for the identity of the release workflow and an entry in the Rekor log.
   - Binaries and SBOMs get a Sigstore bundle (`<file>.sigstore.json`).
   - Images are signed, and their SBOM is attached as a cosign attestation (`spdxjson`).
5. The workflow verifies its own signatures with the real cosign before anything is published (WP-545):
   - For linux/amd64 and linux/arm64, `edgecore/update/cosign_real_test.go` (build tag `cosignreal`) runs the updater's `CosignVerifier` against the collector and its bundle: the signed binary passes with the pinned release identity; the binary changed by one byte, the bundle of another artifact and another identity are refused. A failing or skipped test stops the release.
   - Every bundle passes `cosign verify-blob`; every image passes `cosign verify` and `cosign verify-attestation --type spdxjson`. The release is published only after all four images passed.
   - It then publishes the files on the GitHub release, together with `SHA256SUMS` and one updater manifest per binary (`<binary>.release.json`, holding `downloadUrl`, `signatureBundleUrl` and `checksums.sha256`).

Mandatory signature check in the updater (`edgecore/update`):

- `Checker.Apply` installs an artifact only after its signature has been verified.
- The updater refuses, and leaves the running binary untouched, in these cases:
  - the release has no `signatureBundleUrl`;
  - the bundle cannot be loaded;
  - verification fails;
  - no verifier is configured.
- `CosignVerifier` calls `cosign verify-blob --bundle`. It pins the signer to `release.yml` of this repository on a `v*` tag, with the GitHub Actions OIDC issuer.

Check a release by hand (operators, before an installation or an air-gapped transfer). Every command must exit 0 (`verify-blob` prints `Verified OK`, `verify` the verified signature payload); any other result means the file or image is not the one the release workflow signed. The signer is the release workflow of this repository on a version tag:

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

Images: the signature (`cosign verify`) and the SPDX SBOM attestation (`cosign verify-attestation --type spdxjson`) of all four images `reticora-server`, `reticora-collector`, `reticora-frontend` and `reticora-postgres`:

```bash
for image in reticora-server reticora-collector reticora-frontend reticora-postgres; do
  cosign verify "ghcr.io/datahub-chiemgau/$image:$VERSION" \
    --certificate-identity-regexp "$IDENTITY" \
    --certificate-oidc-issuer "$ISSUER"
  cosign verify-attestation --type spdxjson "ghcr.io/datahub-chiemgau/$image:$VERSION" \
    --certificate-identity-regexp "$IDENTITY" \
    --certificate-oidc-issuer "$ISSUER" > /dev/null
done
```

`cosign verify` prints the image digest (`docker-manifest-digest`); pin that digest (`ghcr.io/datahub-chiemgau/<image>@sha256:…`) instead of the tag, as the installers do in release mode. Without `> /dev/null`, `verify-attestation` prints the attestation; the SBOM is the `predicate` of the in-toto statement in its base64 `payload`.

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
