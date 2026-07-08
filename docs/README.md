# Reticora CMDB Documentation

## Architecture

Reticora CMDB is a multi-tenant Configuration Management Database designed for Managed Service Providers (MSPs) and Internet Service Providers (ISPs).

### Technology Stack

- **Backend**: Go 1.25+, chi v5 router, pgx v5, sqlc
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

### Getting Started

See the root [README.md](../README.md) for setup instructions.
