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

## Monitoring

Probe monitoring (Epic E) adds to the rows of the central cloud. The figures
follow ADR 0002 (`docs/decisions/0002-probe-storage.md`): probe results in
the hypertable `probe_result` with 3-hour chunks, compression after one day,
raw results 14 days, aggregates 5 min (90 days), 1 h and 1 d (400 days),
state changes and daily availability 400 days (PRB-06, E-172). The design
load of an instance is NFR-11: 353 results/s permanently (30.5 million a
day), sized with reserve factor 3 (ingest ≥ 1,060 results/s), and a
resynchronisation peak of 5,000 results/s after an outage (PRB-07).

| Load | vCPU | RAM | Disk (SSD) | IOPS (write) | Notes |
|---|---:|---:|---:|---:|---|
| 353 results/s (NFR-11) | +2 vCPU | +8 GB, `shared_buffers` ≥ 4 GB | +60 GB | ≥ 1,000 | Active 3-hour chunk 0.81–0.96 GB, below 25 % of `shared_buffers` |
| 1,060 results/s (reserve factor 3) | +4 vCPU | +24 GB, `shared_buffers` ≥ 12 GB | +150 GB | ≥ 3,000 | Active 3-hour chunk 2.4–2.9 GB |
| 5,000 results/s (resynchronisation peak) | burst of the row above | as above | as above | ≥ 3,000 | Minutes, not permanent; WAL 2.3–2.9 MB/s |

- **CPU:** the ingest worker and the database writing in blocks of 1,000
  rows; the spike wrote about 100,000 rows/s in one session (ADR 0002,
  result 1), so writing is no CPU bottleneck. The vCPUs are for the ingest,
  state and alarm workers, the compression and aggregate refresh jobs and
  the queries of the monitoring pages.
- **RAM:** the active chunk with its indexes stays within 25 % of
  `shared_buffers` (TimescaleDB guidance). The resident set of the
  monitoring workers is measured with the load test (WP-1084).
- **Disk:** steady state about 15–40 GB at 353/s (raw results 10.7–18.6 GB,
  aggregates compressed about 3.5–5 GB, state changes up to 16 GB, daily
  availability below 1 GB), 45–120 GB at 1,060/s; the rows add headroom for
  the uncompressed aggregates of the first refresh, vacuum and a day of
  growth.
- **WAL and IOPS:** 459–575 bytes of WAL per result: 584–731 MB/h (14–17.5
  GB a day) at 353/s, 1.75–2.2 GB/h at 1,060/s and 8.3–10.4 GB/h during the
  resynchronisation peak. The WAL archive (PITR) and its retention grow by
  the same amount. Writes arrive in a few blocks per second; the IOPS
  figures cover WAL, checkpoints and the compression job with headroom
  (estimate, measured with the load test WP-1084).
- The figures are a local measurement of the spike (TimescaleDB 2.31,
  developer machine) plus calculation; the load test before G3 (WP-1083,
  WP-1084, NFR-11) confirms or corrects them. The metric samples of metric
  probes (SNMP, Redfish, optics) stay in `metric_sample` (MON-01) and are
  sized with the line above.

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

## OpenSearch for search (TEC-12 [P5])

With `RETICORA_SEARCH_BACKEND=opensearch` PostgreSQL stays the source of the
search index; OpenSearch answers queries without field restrictions and
gets the full documents of PostgreSQL: title, summary, the field rows
(`fields.<key>`: columns and searchable custom fields) and the identifiers
(`identifiers`: inventory number, IP and MAC addresses; SRC-02, WP-787).
Queries with restricted field permissions are always answered by
PostgreSQL.

- **After switching to OpenSearch and after each upgrade** that extends the
  mapping, run the reindex job (`POST /api/v1/search/reindex`, job
  `search.reindex`). The server adds new fields to the mapping of the
  existing index on startup (additive), but only the job fills them.
- **After mass changes** that only database triggers apply (assets, network
  interfaces, IP addresses imported or changed in bulk), run the job again.
  OpenSearch learns of such changes only with the next reindex job or the
  next indexing of the document (e.g. an edit of the CI); until then it may
  miss hits by the changed identifiers. PostgreSQL itself is current at
  once, and every OpenSearch hit is checked against it.
- An OpenSearch backend without PostgreSQL (no database profile) keeps the
  fields of phase 1 (title, summary, metadata) and finds nothing by field
  rows or identifiers.

## Discovery collector VM

| Profile | vCPU | RAM | Disk |
|---|---:|---:|---:|
| Collector (any size) | 2 vCPU | 2 GB | 20 GB |

The disk holds the offline spool (COL-05); a collector that is offline for
long periods with many targets needs more.
