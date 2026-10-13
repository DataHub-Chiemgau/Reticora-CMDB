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
| `reservation_sweeper` | `RETICORA_WORKER_RESERVATION_SWEEPER_ENABLED` | Releases expired reservations. |
| `jobs` | `RETICORA_WORKER_JOBS_ENABLED` | Consumes the `JOBS` work-queue stream on NATS (`jobs.<kind>`, TEC-14): one durable consumer per job kind, the database claim runs each job once, a job whose instance died is taken over after its timeout. Only active with NATS. |

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

The OPS-02 workers below do not exist yet as separate runners. Each one
gets its switch, following the convention above, in the work package that
introduces it:

| Planned name | Requirement |
|---|---|
| `offline_detector` | Offline detection for collectors and agents |
| `audit_runner` | Audit jobs |
| `job_runner`, `scheduler` | Shared job framework with a single runner (TEC-14) |
| `notifier` | Notification delivery (NTF) |
| `workflow` | Workflows [P3] |
| `iga` | IGA provisioning tasks [A] |

## Example: dedicated worker instance

API instances switch every worker off:

```bash
RETICORA_WORKER_WEBHOOK_DISPATCHER_ENABLED=false
RETICORA_WORKER_EXPORT_ENABLED=false
RETICORA_WORKER_INGEST_ENABLED=false
RETICORA_WORKER_MONITORING_ENABLED=false
RETICORA_WORKER_RESERVATION_SWEEPER_ENABLED=false
```

The worker instance keeps the defaults. At startup the log shows
`worker started` or `worker disabled` for each worker.
