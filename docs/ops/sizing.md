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

## Document uploads and malware scan (SEC-09, E-42)

Only with the module `documents` (off by default) and its clamd (compose
profile `documents`, Kubernetes component `clamav`):

- **Uploads:** an upload occupies up to twice the upload limit
  (`RETICORA_DOCUMENT_MAX_UPLOAD_MB`, default 50 MiB) at once: the request
  body the frontend nginx buffers (temp files under `/tmp`, a tmpfs in
  Compose, so RAM) and the copy the server checks and stores. Plan
  *concurrent uploads × 2 × upload limit* of extra RAM, e.g. 10 concurrent
  uploads × 2 × 50 MiB = 1 GB.
- **clamd:** the signature database is held in RAM, about 1–1.5 GB today and
  growing; while clamd reloads after a signature update it holds the old and
  the new database at once, so plan **twice** that (3 GB, the limit of the
  Kubernetes component is 4 Gi). The volume of the signatures needs about
  1 GB of disk. A scan streams the version from the object store, its
  memory is bounded by `StreamMaxLength`/`MaxScanSize`.

With the module on, add 4 GB of RAM to the rows of the central cloud.

## Discovery collector VM

| Profile | vCPU | RAM | Disk |
|---|---:|---:|---:|
| Collector (any size) | 2 vCPU | 2 GB | 20 GB |

The disk holds the offline spool (COL-05); a collector that is offline for
long periods with many targets needs more.
