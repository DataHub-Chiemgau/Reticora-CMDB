# Background workers and their switches (OPS-02)

Every background worker of the server has an operator switch of its own.
That way an installation can run the workers on dedicated instances, or
stop a single worker during an incident, without touching the API.

## Convention

- Worker `<name>` is switched by `RETICORA_WORKER_<NAME>_ENABLED`
  (`true`/`false`, default `true`).
- A disabled worker is not started in this process. The API keeps
  serving requests.
- An invalid value (for example `maybe`) stops the start check
  (fail-closed, OPS-01).
- The registry is `config.WorkerNames` in `backend/internal/config/config.go`.
  The server starts its workers through `server.StartWorkers`, which refuses
  names that are not registered or that appear twice.
- A new worker adds its name to `config.WorkerNames` and registers under it
  in `backend/cmd/server/main.go`, in the work package that introduces it.

## Registered workers

| Name | Switch | Task |
|---|---|---|
| `webhook_dispatcher` | `RETICORA_WORKER_WEBHOOK_DISPATCHER_ENABLED` | Re-sends persisted webhook deliveries that are due for a retry. The first attempt of an event is sent by the instance that raised it, which holds a two-minute lease on it; this worker takes the delivery over only after the lease, when that instance died (TEC-14). |
| `export` | `RETICORA_WORKER_EXPORT_ENABLED` | Processes queued asynchronous export jobs. Only active with object storage. A job that has been running for 15 minutes is queued again: its instance died (TEC-14). |
| `ingest` | `RETICORA_WORKER_INGEST_ENABLED` | Consumes `ingest.>` from NATS (REC-01). The API still publishes to the queue. |
| `monitoring` | `RETICORA_WORKER_MONITORING_ENABLED` | Evaluates alert rules and the health of the CIs. |
| `scheduler` | `RETICORA_WORKER_SCHEDULER_ENABLED` | Runs the system jobs on their cron schedules (JOB-02). Every replica runs a scheduler; one leads through a PostgreSQL advisory lock, each job kind also runs under a lock of its own, so scaling (HPA) never runs a job twice. When the leader stops or loses its database session, another replica takes over and catches up on missed slots, at most `MaxCatchUp` per kind (state in `scheduler_run`). Registered: `reservation.expire` (every minute) releases expired reservations; the retention runs (nightly, see below). |
| `events` | `RETICORA_WORKER_EVENTS_ENABLED` | Publishes the domain events of the transactional outbox (`event_outbox`) in sequence order per organization to the NATS stream `RETICORA_EVENTS` (`events.<org_id>.<type>`, 30 days / 10 GB) and to the webhook subscriptions; one instance per organization at a time (EVT-01, EVT-02). Without NATS the webhooks still receive the events. |
| `notifier` | `RETICORA_WORKER_NOTIFIER_ENABLED` | Delivers queued notifications (`notification_delivery`) over their channels — e-mail, in-app, webhook — with retry and exponential backoff (5 attempts), deduplication by key, the recipients' quiet hours and muted categories (critical notifications ignore both); replicas share the queue, a delivery whose instance died is taken over after 10 minutes (NTF-04). |
| `nats_edge` | `RETICORA_WORKER_NATS_EDGE_ENABLED` | Collector NATS endpoint (COL-08): writes the collector CA bundle, the server certificate of `nats-edge` (host names from `RETICORA_NATS_EDGE_HOSTS`, renewed from 60 % of its lifetime) and the generated `edge.conf` into `RETICORA_PKI_DIR` (daily), and answers the endpoint's authorization callout at `RETICORA_NATS_EDGE_URL`: only certificates of the collector CA that are not revoked, of collectors that are not disabled, each confined to its own subjects (docs/betrieb/firewall-matrix.md). Run it on one or more server instances; its keys derive from the master key. |
| `credential_bundles` | `RETICORA_WORKER_CREDENTIAL_BUNDLES_ENABLED` | Answers the collectors' bundle requests (`collector.<org>.<id>.credentials.request` over the NATS endpoint): verifies the presented certificate (collector CA, not revoked, collector not disabled) and returns the credentials of the collector's enabled discovery scopes (without scopes: those of its client and the organization-wide ones), encrypted for the collector's key (ECDH P-256, HKDF, AES-GCM); every delivery is audited (`credential.bundle_delivered`, COL-02). Needs `RETICORA_NATS_URL`. |
| `discovery_scopes` | `RETICORA_WORKER_DISCOVERY_SCOPES_ENABLED` | Serves the collectors' discovery scopes over the NATS endpoint (DIS-09): `collector.<org>.<id>.scopes.request` returns the enabled scopes of a collector that is not disabled, `…scopes.result` records a run (`last_run_at`, `last_result`, `next_run_at`), `…subnets.report` turns the local subnets a collector reports after enrollment into `scope_suggested` review items (accepting one creates the scope). Needs `RETICORA_NATS_URL`. |
| `jobs` | `RETICORA_WORKER_JOBS_ENABLED` | Consumes the `JOBS` work-queue stream on NATS (`jobs.<kind>`, TEC-14): one durable consumer per job kind, the database claim runs each job once, a job whose instance died is taken over after its timeout. Only active with NATS. |

## Retention runs (NFR-06, AUD-09)

The scheduler deletes expired data every night, per organization and in the
organization's transaction. A run that deleted something writes its own
audit entry (`retention.*`).

| Run | Data | Kept for | Setting (minimum) |
|---|---|---|---|
| `retention.audit_log` (03:17) | `audit_log` | 10 years | `RETICORA_RETENTION_AUDIT_YEARS` (10) |
| `retention.history` (03:27) | `ci_change`, `entity_change`, `location_change` | 3 years | `RETICORA_RETENTION_HISTORY_DAYS` (1095) |
| `retention.soft_deleted` (03:37) | soft-deleted CIs | 90 days | `RETICORA_RETENTION_SOFT_DELETE_DAYS` (90) |
| `retention.review_items` (03:47) | resolved review items | 1 year | `RETICORA_RETENTION_REVIEW_ITEM_DAYS` (365) |

A value below the minimum stops the start. The audit log keeps its chain
verifiable: the run removes the oldest entries of a chain (never the newest)
and appends an `audit.retention` entry with the sequence number and hash of
the last removed entry, from which verification continues. The database
allows the deletion only for entries older than ten years and only inside a
retention run (migration 000119). A soft-deleted CI that is still referenced
(asset, assignment, ticket) stays. Events (stream, 30 days), metrics
(TimescaleDB policy, 400 days), logs (30 days) and backups (35 days) expire
where they are kept.

## Key rotation (SEC-06)

| Run | When | What |
|---|---|---|
| `rotation.master_key` | nightly 02:23 | Rewraps every data key (organization DEKs, notification channel keys) that a previous master key wrapped. |
| `rotation.org_dek` | nightly 02:33 | Replaces an organization's DEK after one year and re-encrypts its credentials in one transaction; audited as `key.org_dek_rotated`. |
| `rotation.webhook_secrets` | every 15 minutes | Ends the 24-hour overlap of rotated webhook secrets. |

Master key rotation without downtime: set the new key as `RETICORA_MASTER_KEY`
and the old one in `RETICORA_MASTER_KEY_PREVIOUS` (comma-separated for
several), restart, and let `rotation.master_key` run (or wait for the night).
Every wrapped data key names the master key that wrapped it. Once the run
reports that all data keys use the current key, remove the old key from
`RETICORA_MASTER_KEY_PREVIOUS`: it is retired. Webhook secrets rotate with
`POST /api/v1/webhooks/{id}/rotate-secret`, API keys with
`POST /api/v1/api-keys/{id}/rotate`; session signing keys carry a `kid` and
rotate in the identity layer.

## Metrics and alerts (OPS-05)

Every worker exports these series, labelled `worker`:

| Metric | Meaning |
|---|---|
| `reticora_worker_enabled` | 1 when the switch is on in this instance |
| `reticora_worker_up` | 1 while the loop runs; 0 when disabled or stopped |
| `reticora_worker_runs_total` | Completed cycles (poll, sweep, batch) |
| `reticora_worker_errors_total` | Failures inside the cycles |
| `reticora_worker_last_success_timestamp_seconds` | Time of the last cycle without error |
| `reticora_worker_backlog` | Waiting items, sampled every 30 s: queued exports (`export`), due deliveries (`webhook_dispatcher`), pending and unacknowledged messages (`ingest`) |

`deploy/monitoring/worker-rules.yaml` defines these alerts:

| Alert | Fires when |
|---|---|
| `ReticoraWorkerStopped` | A switched-on worker is not running, for 5 minutes |
| `ReticoraWorkerErrors` | More than 5 failures in 15 minutes |
| `ReticoraWorkerNoSuccess` | No successful cycle for 30 minutes |
| `ReticoraWorkerBacklog` | Backlog above 1000 |
| `ReticoraWorkerBacklogGrowing` | Backlog grows for 30 minutes |

`deploy/monitoring/alertmanager.yaml` routes all alerts to the operators. Critical alerts also go to the on-call webhook. The product's notification channel (NTF) is added with WP-137. The Grafana dashboard `deploy/grafana/dashboard-overview.json` shows runs, errors, backlog and running instances.

## Planned workers

The OPS-02 workers below do not exist yet as separate runners. The job
runner of OPS-02 is the `jobs` worker; scheduled system jobs register with
the `scheduler` instead of getting a worker of their own. Each one
gets its switch, following the convention above, in the work package that
introduces it:

| Planned name | Requirement |
|---|---|
| `offline_detector` | Offline detection for collectors and agents |
| `audit_runner` | Audit jobs |
| `workflow` | Workflows [P3] |
| `iga` | IGA provisioning tasks [A] |

## Example: dedicated worker instance

API instances switch every worker off:

```bash
RETICORA_WORKER_WEBHOOK_DISPATCHER_ENABLED=false
RETICORA_WORKER_EXPORT_ENABLED=false
RETICORA_WORKER_INGEST_ENABLED=false
RETICORA_WORKER_MONITORING_ENABLED=false
RETICORA_WORKER_SCHEDULER_ENABLED=false
RETICORA_WORKER_JOBS_ENABLED=false
RETICORA_WORKER_EVENTS_ENABLED=false
RETICORA_WORKER_NOTIFIER_ENABLED=false
```

The worker instance keeps the defaults. At startup the log shows
`worker started` or `worker disabled` for each worker.
