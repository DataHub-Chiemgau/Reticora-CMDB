# Reticora CMDB Documentation

## Architecture

Reticora CMDB is a multi-tenant Configuration Management Database designed for Managed Service Providers (MSPs) and Internet Service Providers (ISPs).

### Technology Stack

- **Backend**: Go 1.25+, chi v5 router, pgx v5 (hand-written SQL)
- **Frontend**: React 18, TypeScript, Vite, TanStack Query v5, Zustand, React Router v6
- **Database**: PostgreSQL 16 with Row-Level Security
- **Messaging**: NATS JetStream
- **Cache**: Redis 7
- **Observability**: OpenTelemetry, Prometheus metrics

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
RS256 session token. Every subsequent request carries that token as
a bearer token in the `Authorization` header; the client refreshes it via
`POST /api/v1/auth/refresh` once on a 401 and retries the request. Server-side
the token signature is verified by `middleware.AuthMiddlewareWithVerifier`;
only `/api/v1/auth/{config,login,callback,refresh}` are unauthenticated.

**Tenant resolution:** `TenantMiddleware` derives the organization from the
verified session token. The `X-Organization-ID` header is only consulted for
requests that carry no claims at all (the unverified `--no-db` development mode
and the header-authenticated collector), and it can never override a token
claim — otherwise any authenticated caller could read and write another
tenant's data by changing a header. Handlers therefore read the tenant from the
request context only. Credentials, which store decryptable secret material,
additionally ignore `organization_id` in the request body and answer 401 when
the context is missing.

**Entitlement enforcement:** `entitlement.Service` resolves the effective plan
per tenant (falling back to `RETICORA_DEFAULT_PLAN`) and caches it for 30
seconds. Its middleware maps add-on route prefixes to features and answers with
HTTP 403 when the plan does not include them; core routes (CIs, auth, users,
audit, entitlements) are never gated. Record limits are enforced in the domain
service (`ci.Service.Create`), so limit violations surface as 403 as well.
Repository errors deny access (fail-closed). Plan matrix:

| Plan | Features |
|------|----------|
| essential | cmdb, discovery, inventory |
| standard | + documents, stocktake, ticketing, export, webhooks |
| pro | + monitoring, workflow_forms |
| enterprise | + iga, endpoint_agent, workflow_forms, compliance |


**Permissions and SLA:** migration 0026 turns permissions into data instead of
leaving them as ad-hoc JSON strings. `permission` is the global catalogue,
`role_permission` grants catalogue entries to tenant roles, and the effective
permissions endpoint also folds in existing custom-role JSON grants so old data
continues to work while new code can validate against the catalogue. SLA policy
rows define priority-based response and resolution targets, while `ticket_sla`
stores the applied policy and clock outcomes. Ticket creation, first visible
response and resolution transitions update that state so breach listings are
computed from persisted timestamps, not from UI-only heuristics. The first
implementation treats `business_calendar` as a policy flag and still calculates
due times in elapsed minutes; that preserves the contract while leaving calendar
working-hours expansion for a dedicated scheduler.


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

**Reconciliation and topology:** discovery runs are recorded as jobs
(`/api/v1/discovery/jobs`). Findings that cannot be matched to an existing CI
with sufficient confidence are not applied directly; they land in the review
queue (`/api/v1/discovery/review`) where they are accepted or rejected by an
operator. Relationships derived from discovery evidence (LLDP/CDP neighbours,
hypervisor placement, power feeds) are written as regular
`ci_relationship` rows, and a suppression list keeps operator-rejected pairs
from being re-derived on the next run. `/api/v1/topology` and
`/api/v1/topology/cis/{id}/neighbors` serve the resulting graph.

**Binaries (`backend/cmd/`):**

- `server` — Main API server (HTTP + GraphQL BFF).
- `collector` — On-premise discovery collector.
- `agent` — Endpoint agent for server/client telemetry.

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
`/tickets`, `/users`, `/permissions`, `/slas`, `/forms`, `/workflows` and `/compliance`. Every CI is deep-linkable: list rows, topology nodes,
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

**Production serving:** The built SPA is served via Nginx (`nginx.conf`) in a Docker container with proper SPA fallback routing.

### Deployment

Reticora is deployed on **Kubernetes** in an EU region with infrastructure managed by **Terraform**.

**Container images:**

- Backend server: Multi-stage Alpine build (`backend/Dockerfile`), runs as `nobody`.
- Frontend: Nginx-served static SPA (`frontend/Dockerfile`).

**Kubernetes (Kustomize):**

Manifests in `deploy/k8s/`:

- `deployment.yaml` — Server deployment (2 replicas, health probes, resource limits).
- `service.yaml` — ClusterIP service.
- `kustomization.yaml` — Namespace `reticora`, common labels.

**Infrastructure services (docker-compose for local dev):**

| Service | Image | Purpose |
|---------|-------|---------|
| PostgreSQL + TimescaleDB | timescale/timescaledb:latest-pg16 | Primary database with time-series |
| NATS | nats:2.10-alpine | Event bus (JetStream) |
| Redis | redis:7-alpine | Cache, sessions, rate-limiting |
| MinIO | minio/minio:latest | S3-compatible object storage |
| Keycloak | keycloak:24.0 | OIDC identity provider |

**Terraform:**

`deploy/terraform/main.tf` provides the module scaffold for cloud provisioning (target region: `eu-central-1`).

**Grafana/Monitoring:**

Dashboards and alerting configuration in `deploy/grafana/`.

**Environment configuration:**

The server is configured via environment variables:
- `RETICORA_PORT` — HTTP listen port (default 8080).
- `RETICORA_DATABASE_URL` — PostgreSQL connection string.
- `RETICORA_NATS_URL` — NATS server URL.
- `RETICORA_REDIS_URL` — Redis connection string.
- `RETICORA_ENVIRONMENT` — Environment name (development/staging/production).
- `RETICORA_DEFAULT_PLAN` — Plan applied to tenants without explicit entitlements (default `essential`).
- `RETICORA_ENTITLEMENT_ENFORCEMENT` — Set to `false` to disable feature/limit enforcement (default `true`).

### CI/CD

Continuous Integration runs on **GitHub Actions** (`.github/workflows/ci.yml`), triggered on push and pull requests to `main`.

**Backend job:**

1. Checkout code.
2. Setup Go 1.25.
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

**Planned additions:**

- Docker image build and push to container registry on tag/release.
- Kubernetes deployment via GitOps (ArgoCD or Flux).
- Database migration validation in CI.
- Integration tests against docker-compose services.
- Security scanning (Trivy, CodeQL).
- Dependency update automation (Dependabot/Renovate).

### Getting Started

See the root [README.md](../README.md) for setup instructions.
