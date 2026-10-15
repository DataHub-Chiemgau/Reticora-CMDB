# Sizing

Reticora is designed for 1,000–10,000 objects (CIs, assets, locations) per
customer with headroom up to 50,000 (CH15, NFR-01; the range was confirmed on
2026-10-15, decision E-17: budgets apply at 10,000 objects, the headroom up
to 50,000 works without a budget commitment). The installer checks the host
against the minimum of its profile and warns when a resource is below it
(INS-07); it does not abort.

## Supported platforms

Ubuntu 22.04 LTS, Ubuntu 24.04 LTS and Debian 12 (CH22) with Docker Engine
≥ 24 and Docker Compose v2. The installer refuses other systems;
`RETICORA_ALLOW_UNSUPPORTED_PLATFORM=1` continues without support.

## Central cloud (all-in-one Docker Compose)

PostgreSQL/TimescaleDB, Keycloak, NATS, Redis, MinIO, server and frontend on
one host. Disk means SSD; it holds the database, the object store (exports,
documents, backups before off-site transfer) and the container images.

| Objects | vCPU | RAM | Disk (SSD) | Notes |
|---:|---:|---:|---:|---|
| ≤ 10.000 | 4 vCPU | 8 GB | 100 GB | Minimum (INS-07); checked by `install-cloud.sh` |
| ≤ 25.000 | 8 vCPU | 16 GB | 200 GB | Increase `shared_buffers` to 4 GB |
| ≤ 50.000 | 16 vCPU | 32 GB | 400 GB | Headroom of CH15; consider a separate database host and OpenSearch for search |

Metric samples (monitoring) grow with the number of polled CIs and the
retention; plan an extra 1 GB per 1,000 polled CIs and 30 days of retention.

## Discovery collector VM

| Profile | vCPU | RAM | Disk |
|---|---:|---:|---:|
| Collector (any size) | 2 vCPU | 2 GB | 20 GB |

The disk holds the offline spool (COL-05); a collector that is offline for
long periods with many targets needs more.
