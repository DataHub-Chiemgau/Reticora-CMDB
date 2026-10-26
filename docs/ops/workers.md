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
- A set `RETICORA_WORKER_<NAME>_ENABLED` whose name is not a registered
  worker (a typo, a planned worker) has no effect; the server logs it at
  start as `configuration warning`.
- The registry is `config.WorkerNames` in `backend/internal/config/config.go`.
  The server builds its workers in `buildWorkers`
  (`backend/cmd/server/workers.go`) and starts them through
  `server.StartWorkers`, which refuses names that are not registered or that
  appear twice. A worker that runs as a system job of the scheduler
  (`offline_detector`) is registered in `systemJobs` in the same file only
  while its switch is on.
- A new worker adds its name to `config.WorkerNames`, registers under it in
  `buildWorkers` (or `systemJobs`) and records its cycles (see
  [Metrics and alerts](#metrics-and-alerts-ops-05)), in the work package that
  introduces it. Its switch goes into `deploy/docker-compose/docker-compose.yml`
  (from `.env`) and `deploy/k8s/base/deployment.yaml`;
  `TestEveryWorkerExportsRunAndErrorMetrics` and
  `TestEveryWorkerSwitchIsDeployable` (`backend/cmd/server/workers_metrics_test.go`)
  fail for a registered worker without them.

## Registered workers

| Name | Switch | Task |
|---|---|---|
| `webhook_dispatcher` | `RETICORA_WORKER_WEBHOOK_DISPATCHER_ENABLED` | Re-sends persisted webhook deliveries that are due for a retry. The first attempt of an event is sent by the instance that raised it, which holds a two-minute lease on it; this worker takes the delivery over only after the lease, when that instance died (TEC-14). |
| `export` | `RETICORA_WORKER_EXPORT_ENABLED` | Processes queued asynchronous export jobs. Only active with object storage. A job that has been running for 15 minutes is queued again: its instance died (TEC-14). |
| `ingest` | `RETICORA_WORKER_INGEST_ENABLED` | Consumes `ingest.>` from NATS (REC-01): one consumer per ingest lane (`RETICORA_INGEST_LANES`, default 4), the consumer of the releases before the lanes and lanes left by a higher lane count; at most 6 batches at a time per instance. The API still publishes to the queue. It also answers the collectors' records, heartbeats and metric samples over the NATS endpoint (`collector.<org>.<id>.ingest`, `….heartbeat`, `….metrics`, COL-08, MON-02; the samples are assigned in the collector's client and site scope and need the monitoring entitlement); a request of an unknown or disabled collector is refused before it is handled and changes nothing (COL-04). Only an instance running this worker reports the backlog per lane in `/readyz` (`details.ingest`, its last 30-s sample); every instance running it holds up to 1,542 identity locks, which sizes the PostgreSQL lock table. See [`docs/betrieb/ingest-spuren.md`](../betrieb/ingest-spuren.md). |
| `monitoring` | `RETICORA_WORKER_MONITORING_ENABLED` | Evaluates alert rules and the health of the CIs. |
| `scheduler` | `RETICORA_WORKER_SCHEDULER_ENABLED` | Runs the system jobs on their cron schedules (JOB-02). Every replica runs a scheduler; one leads through a PostgreSQL advisory lock, each job kind also runs under a lock of its own, so scaling (HPA) never runs a job twice. When the leader stops or loses its database session, another replica takes over and catches up on missed slots, at most `MaxCatchUp` per kind (state in `scheduler_run`). Registered: `reservation.expire` (every minute) releases expired reservations; the retention runs (nightly, see below); the notice runs `entitlement.notices` and `certificate.notices` (daily, see below). |
| `events` | `RETICORA_WORKER_EVENTS_ENABLED` | Publishes the domain events of the transactional outbox (`event_outbox`) in sequence order per organization to the NATS stream `RETICORA_EVENTS` (`events.<org_id>.<type>`, 30 days / 10 GB) and to the webhook subscriptions; one instance per organization at a time (EVT-01, EVT-02). Without NATS the webhooks still receive the events. |
| `notifier` | `RETICORA_WORKER_NOTIFIER_ENABLED` | Delivers queued notifications (`notification_delivery`) over their channels — e-mail, in-app, webhook — with retry and exponential backoff (5 attempts), deduplication by key, the recipients' quiet hours and muted categories (critical notifications ignore both); replicas share the queue, a delivery whose instance died is taken over after 10 minutes (NTF-04). |
| `nats_edge` | `RETICORA_WORKER_NATS_EDGE_ENABLED` | Collector NATS endpoint (COL-08): writes the collector CA bundle, the server certificate of `nats-edge` (host names from `RETICORA_NATS_EDGE_HOSTS`, renewed from 60 % of its lifetime) and the generated `edge.conf` into `RETICORA_PKI_DIR` (daily), and answers the endpoint's authorization callout at `RETICORA_NATS_EDGE_URL`: only certificates of the collector CA that are not revoked, of collectors that are not disabled, each confined to its own subjects, for at most 15 minutes per authorization (docs/betrieb/firewall-matrix.md). A disable, announced on the internal NATS (`discovery.collector.disabled`, also by other replicas), makes one instance cut the collector's open connections through the endpoint's system account within seconds; a failure is logged as `discovery.collector.kick_failed` and retried. The first connection with a renewed certificate revokes its predecessors; hourly it revokes predecessors whose successor was issued 7 days ago (docs/betrieb/collector-ca.md). Run it on one or more server instances; its keys derive from the master key. |
| `credential_bundles` | `RETICORA_WORKER_CREDENTIAL_BUNDLES_ENABLED` | Answers the collectors' bundle requests (`collector.<org>.<id>.credentials.request` over the NATS endpoint): verifies the presented certificate (collector CA, not revoked, collector not disabled) and returns the effective credentials of the collector's enabled discovery scopes (a scope with `credential_ids` those, a scope without them the organization-wide ones and those of its client; only credentials that are organization-wide or of the scope's client; without an enabled scope none, E-67), encrypted for the collector's key (ECDH P-256, HKDF, AES-GCM); every delivery is audited (`credential.bundle_delivered`, COL-02). It also renews the collectors' certificates (`collector.<org>.<id>.certificate.renew`, queue group): signed with the current key, from 60 % of the lifetime, audited as `collector.certificate_issued` (COL-04, docs/betrieb/collector-ca.md). Needs `RETICORA_NATS_URL`. |
| `discovery_scopes` | `RETICORA_WORKER_DISCOVERY_SCOPES_ENABLED` | Serves the collectors' discovery scopes over the NATS endpoint (DIS-09): `collector.<org>.<id>.scopes.request` returns the enabled scopes of a collector that is not disabled, `…scopes.result` records a run (`last_run_at`, `last_result`, `next_run_at`), `…subnets.report` turns the local subnets a collector reports after enrollment into `scope_suggested` review items (accepting one creates the scope). A request of an unknown or disabled collector is refused before it is handled and changes nothing (COL-04). Needs `RETICORA_NATS_URL`. |
| `jobs` | `RETICORA_WORKER_JOBS_ENABLED` | Consumes the `JOBS` work-queue stream on NATS (`jobs.<kind>`, TEC-14): one durable consumer per job kind, the database claim runs each job once, a job whose instance died is taken over after its timeout. Only active with NATS. |
| `offline_detector` | `RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED` | The CI offline detector (REC-09, LCY-05), the system job `ci.offline_detector` of the scheduler (every 5 minutes, on the leading replica): an observable, discovered CI whose `last_seen_at` is older than three times the interval of its discovery scope becomes `unknown` (`ci.offline`), and `active` again once seen (`ci.online`). Switched off, the job is not registered on this instance; since a replica with the switch on can lead, switch it off on every instance. The former switch `RETICORA_OFFLINE_DETECTOR_ENABLED` still applies while `RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED` is not set and is logged as deprecated; next to it, it is ignored. Needs the database. |

## Destinations of the notifier (SEC-08)

The `notifier` sends over the central SMTP server of the installation
(`central_smtp`, `RETICORA_SMTP_*`), which is the operator's configuration
and not restricted. Webhook channels and an organization's own SMTP server
(provider `smtp`) are user input and pass the egress policy:

- Without `RETICORA_EGRESS_ALLOW_PRIVATE` (default `false`, SaaS) an own SMTP
  server must be a public host on port 25, 465 or 587. Saving a channel
  with an IP literal or name of a private, loopback, link-local or
  unique-local network (`10.0.0.5`, `127.0.0.1`, `fd00::/8`, `localhost`,
  `*.internal`) or another port answers 422 with the field (`host`, `port`)
  and names the setting.
- Every connection resolves the name once, refuses it when one of its
  addresses is internal and connects to the checked address, so a name
  that points into the internal network later (DNS rebinding) is not
  reached; the delivery fails with `egress: destination is not allowed`.
- Dedicated, on-premises and air-gapped installations whose SMTP relay
  lives in the internal network set `RETICORA_EGRESS_ALLOW_PRIVATE=true`
  on the server instances (API and `notifier`): private and loopback
  destinations and other ports are then accepted. Cloud metadata endpoints
  (`169.254.169.254`, `fd00:ec2::254`, `100.100.100.200`) stay blocked in
  every mode. The same setting applies to webhooks and IGA connectors.

A PATCH of a channel that changes host, port, TLS mode or user name of an
SMTP channel, or the url of a webhook channel, needs the password or secret
again: the placeholder `********` is refused with 400 (field `password` or
`secret`).

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
(TimescaleDB policy, 400 days), container logs (30 days, see
[Container logs](#container-logs-nfr-06)) and backups (35 days) expire where
they are kept.

### Re-chaining with migration 000158 (AUD-04)

Migration 000158 makes `audit_entry_hash` hash the canonical JSON as UTF-8
(WP-423). Entries with a backslash in their canonical JSON were hashed
differently before, so the migration re-chains each organization's chain in
seq order, in batches of 1000, from its first such entry; chains without one
keep their hashes. The re-chain stops at an entry that was not intact before
(changed, missing or wrongly linked), so `audit-verify` still reports that
break. The migration runs in one transaction and holds a SHARE ROW EXCLUSIVE
lock on `audit_log`: audited writes wait until it has finished, reads go on.

Run time for 1,000,000 entries of one organization (PostgreSQL 16.15 with
TimescaleDB 2.31.0, 4 shared vCPUs, about 0.7 kB of changes per entry):

| Case | Up | Down |
|---|---|---|
| No entry with a backslash (every hash is checked, none rewritten) | about 40 s | about 45 s |
| Backslash in the first entry (all 1,000,000 entries re-chained) | 2 min 29 s | 2 min 35 s |

The time grows linearly with the number of entries. Every re-chained entry
gets a new row version, so `audit_log` needs up to its size again in disk
space until autovacuum has run. Check the chains afterwards with
`audit-verify -all`. The down migration re-chains back the same way; an entry
with a quote or a control character, storable only since 000158, ends the
re-chain of its organization, and the restored function cannot verify that
chain.

## Container logs (NFR-06)

The logs of the containers (server, frontend, Keycloak, databases, NATS, …)
can hold personal data such as user names, e-mail and IP addresses. They are
kept 30 days and then deleted (NFR-06, decision E-77 Option 1).

| Deployment | Mechanism | Set up by |
|---|---|---|
| Compose on a host with systemd-journald (Ubuntu 22.04/24.04, Debian 12) | every service logs to the journal (`x-logging` in `deploy/docker-compose/docker-compose.yml`, driver `journald`); the drop-in `/etc/systemd/journald.conf.d/reticora.conf` sets `MaxRetentionSec=30day` and `MaxFileSec=1day` | `install-cloud.sh` |
| Compose without systemd-journald (e.g. Docker in a container without systemd) | `deploy/docker-compose/docker-compose.logging-json.yml`: json-file with `max-size: 20m` and `max-file: 5` for every service, selected through `COMPOSE_FILE` in `.env` | `install-cloud.sh` (fallback) |
| Kubernetes | node rotation and a log backend with 30 days retention | operator (below) |

**journald.** `MaxRetentionSec` deletes a journal file once its first entry
is older than 30 days, checked when journald starts a new file.
`MaxFileSec=1day` starts a new journal file every day, so the entries go
after about 30 days (29 to 31); with journald's default of one file per month
they could stay up to a month longer. journald also keeps its size
limit (`SystemMaxUse`, by default 10 % of the file system, at most 4 GB).
journald reads the drop-in only at its start, so the installer restarts
`systemd-journald` when it writes or changes it.

**The drop-in applies to the whole host.** It limits the entire journal, not
only the containers of Reticora: system services, the kernel, SSH logins and
other containers on the host lose their entries after 30 days as well. The
installer says so when it writes the drop-in. A host whose other logs must be
kept longer forwards them elsewhere (e.g. rsyslog) or runs Reticora on a
host of its own.

**Settings of the operator.** The installer does not loosen a stricter
setting: a `MaxRetentionSec` of at most 30 days or a `MaxFileSec` of at most
one day in `journald.conf` or another drop-in stays in effect, `reticora.conf`
then leaves that key out and is not written (or removed) when it would set
nothing; a stricter value written into `reticora.conf` itself is kept.
Otherwise the installer rewrites `reticora.conf` on every run; keep your own
settings in a drop-in of your own (e.g. `50-site.conf`). The drop-ins apply in
the order of their file names: one sorting after `reticora.conf` with a longer
`MaxRetentionSec` overrides it, and the installer warns about that. Writing
the drop-in needs root or sudo; without it the installer uses the json-file
fallback and says so.

**Reading the logs.** `docker compose logs <service>` (in
`deploy/docker-compose`) and `docker logs <container>` read both drivers;
the journal also answers `journalctl CONTAINER_NAME=docker-compose-server-1`.
journald limits the messages per service (by default 10,000 per 30 seconds,
scaled by the free disk space); all containers count as `docker.service`.
When a burst of the server exceeds it, journald logs `Suppressed … messages
from docker.service` and drops the lines; raise `LogRateLimitIntervalSec=` /
`LogRateLimitBurst=` in a drop-in for `docker.service` (systemd 240 and later)
if that happens.

**Fallback without systemd-journald.** The journald driver cannot start a
container where no systemd-journald runs. The installer then writes
`COMPOSE_FILE=docker-compose.yml[:docker-compose.override.yml]:docker-compose.logging-json.yml`
into `.env` (the override only with `--build-from-source`), merges the file
into its own compose calls and warns. `docker compose --env-file .env …` in
`deploy/docker-compose` merges it as well (`COMPOSE_FILE` is relative to the
current directory). The rotation bounds the size (at most 5 × 20 MB per
container), not the age: on a quiet service a line can stay longer than 30
days. For the 30 days run the stack on a host with systemd-journald; a re-run
of the installer there switches to journald and removes `COMPOSE_FILE` again.
A `COMPOSE_FILE` of your own without the rotation stops the installer on such
a host.

**Kubernetes.** The manifests do not configure logs: the container runtime
writes them to the nodes (`/var/log/pods`), where the kubelet rotates them by
size. The operator of the cluster provides:

- the node rotation through the kubelet configuration, e.g.
  `containerLogMaxSize: 20Mi` and `containerLogMaxFiles: 5`, which bounds the
  size on the nodes but not the age;
- a log backend that collects the pod logs and deletes them after 30 days,
  e.g. Loki with `retention_period: 720h`, an index lifecycle policy with a
  delete phase after 30 days in Elasticsearch/OpenSearch, or the retention
  of the cloud provider's logging service.

Without such a backend the 30 days of NFR-06 are not met on Kubernetes.

## Notice runs (JOB-02, ENT-07, COL-04)

Two daily runs of the scheduler announce what would otherwise stop
unannounced: an entitlement reaching its `valid_until` and a collector
certificate that is about to end. Each notice is a domain event of the
outbox and goes out like every event to the event stream, the webhook
subscriptions and the notifications (NTF-05).

| Run | When | Event | Recipients of the notification |
|---|---|---|---|
| `entitlement.notices` | daily 04:13 | `entitlement.warning` 14, 7 and 1 day before `valid_until` of an enabled entitlement (`feature`, `valid_until`, `days_left`); `entitlement.expired` once after it (`feature`, `expired_at`) | users with `user:manage` |
| `certificate.notices` | daily 04:23 | `certificate.expiring` (`name`, `expires_at`, `fingerprint`; for a collector also `collector_id`, `client_id`, `site_id`) | users with `discovery:manage` in the collector's client and site; for the CA in the organization |

- Each notice is sent once. The run records it in `notice_sent`
  (organization, event type, key) in the transaction that writes the event;
  the outbox keeps no key and its cleanup removes published events after
  30 days, so a later run does not send it again. The key of a warning is
  the entitlement, its `valid_until` and the threshold, that of the expiry
  the entitlement and its `valid_until`: an extended `valid_until` is warned
  of again at 14, 7 and 1 day.
- Missed runs are caught up once (scheduler). The run then sends only the
  youngest due threshold: a first run six days before `valid_until` sends
  the warning of 7 days, not the one of 14 days as well.
- A collector renews its certificate from 60 % of its 90 days
  (docs/betrieb/collector-ca.md) and checks every 24 hours. A certificate
  counts as expiring when the collector has not renewed it two days after
  that point (day 56, 34 days before its end): the collector is offline,
  cannot reach the endpoint or is refused. Disabled collectors and revoked
  certificates are left out.
- The active collector CA counts as expiring from 60 % of its lifetime (6 of
  its 10 years); every organization with a collector that holds a
  certificate receives one notice. The CA roll-over is an operator task.

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
| `reticora_worker_backlog` | Waiting items, sampled every 30 s: queued exports (`export`), due deliveries (`webhook_dispatcher`), pending and unacknowledged messages (`ingest`, all lanes; per lane in `reticora_ingest_lane_backlog`), due queued jobs of the kinds the queue runs (`jobs`) |

A cycle is one real pass of the worker, not a message and not an idle tick
that checks nothing: a cycle without failure moves the last-success time, so
it must only succeed while the worker could do its work. A failed backlog
sample also counts as an error.

| Worker | One cycle | Failures of the cycle | Backlog |
|---|---|---|---|
| `webhook_dispatcher` | One claim of the due deliveries (every 15 s) | The claim fails | Due deliveries |
| `export` | Reclaim of stale jobs and processing of the pending ones (every 2 s) | Each failed job or storage step | Queued exports |
| `ingest` | One fetch of a lane (up to 1 s) and its batch | The fetch fails | Pending and unacknowledged messages |
| `monitoring` | One evaluation of every enabled rule (every minute) | Each failing step | None: every rule is evaluated in each cycle, nothing waits |
| `scheduler` | One tick of the leading replica (every 15 s): the due slots of every job kind | Each kind whose slot or run failed. A replica that does not lead records no cycle | None: missed slots are caught up (`MaxCatchUp`), they do not queue |
| `events` | One pass over the pending outbox events of every organization (every second) | The pass fails | None: each pass publishes everything pending; events wait only while publishing fails, which the errors report |
| `notifier` | One claim and delivery of the due notifications (every 5 s) | The claim or a delivery fails | None: each cycle delivers everything due; deliveries wait only for their backoff or the recipient's quiet hours, by design |
| `jobs` | One sweep (every minute): the stream `JOBS` answers, the queued jobs due for longer than a minute are announced again | The stream does not answer, the listing or an announcement fails, and since the previous sweep: each failed attempt of a job (as with `export`), each storage error of a claim or result, each error of a consumer | Due queued jobs of the kinds the queue runs; the other kinds of the job table are tracked for `export` and the `scheduler` |
| `nats_edge` | One pass (at start, then every minute): the database answers, the connections of the authorization callout and of the cut of disabled collectors answer a round trip, neither service has stopped | Each failing check, and since the previous pass: each failed PKI export (daily), sweep of superseded certificates (hourly), callout answer and cut | None: it answers the callout requests of the endpoint, nothing queues on the server |
| `credential_bundles` | One pass (at start, then every minute): the internal NATS connection answers a round trip, the database answers, the certificate renewal has not stopped | Each failing check, and since the previous pass: each bundle or renewal that failed on the server's side (`bundle unavailable`, `renewal_unavailable`) and each reply that could not be sent. A refused collector (unknown, disabled, revoked) is an answer, not a failure | None: requests and replies, an unanswered request times out at the collector |
| `discovery_scopes` | One pass (at start, then every minute): the internal NATS connection answers a round trip, the database answers | Each failing check, and since the previous pass: each collector lookup that failed (not: unknown or disabled) and each reply that could not be sent | None, as `credential_bundles` |
| `offline_detector` | One run of `ci.offline_detector` (every 5 minutes, on the leading scheduler) | The run fails | None: a scheduled run, nothing queues |

The `offline_detector` runs in the loop of the `scheduler`: it exports
`reticora_worker_runs_total`, `reticora_worker_errors_total` and
`reticora_worker_last_success_timestamp_seconds`, not `enabled`, `up` or
`backlog`; whether it runs is `reticora_worker_up{worker="scheduler"}`. A
failed run also counts as an error of the `scheduler` and is stored in
`scheduler_run`.

`deploy/monitoring/worker-rules.yaml` defines these alerts:

| Alert | Fires when |
|---|---|
| `ReticoraWorkerStopped` | A switched-on worker is not running, for 5 minutes |
| `ReticoraWorkerErrors` | More than 5 failures in 15 minutes |
| `ReticoraWorkerNoSuccess` | A running worker has had no cycle without error for 35 minutes, or none since its start for 30 minutes |
| `ReticoraWorkerBacklog` | Backlog above 1000 |
| `ReticoraWorkerBacklogGrowing` | Backlog grows for 30 minutes |

`ReticoraWorkerNoSuccess` relies on every worker recording each cycle, an
idle one as well (`observability.ObserveWorkerRun`). A worker that has not
completed a cycle without error since its process started has no
`reticora_worker_last_success_timestamp_seconds` series; the rule starts from
`reticora_worker_up` and only removes workers with a success in the last
5 minutes, so it fires for such a worker too, 30 minutes after the start
(also after a restart). Every worker started through `server.StartWorkers`
records a cycle at least every minute (the `scheduler` on its leading
replica; the rule takes the latest success of all instances), so a working
one never fires; one whose dependency (database, NATS, the edge endpoint)
stays unreachable fires after 35 minutes, idle or not. The cycles of all
registered workers are checked by `TestEveryWorkerExportsRunAndErrorMetrics`.

`deploy/monitoring/alertmanager.yaml` routes all alerts to the operators, by e-mail and through the NTF bridge to the notification channels of the operator organization (below). Critical alerts also go to the on-call webhook. The Grafana dashboard `deploy/grafana/dashboard-overview.json` shows runs, errors, backlog and running instances.

### Alert delivery

The alert chain of an installation, Prometheus → Alertmanager → NTF:

1. Prometheus scrapes `/metrics` of every server instance (worker, HTTP and
   break-glass series).
2. It evaluates the rules of `deploy/monitoring/worker-rules.yaml`,
   `slo-rules.yaml` and `operator-rules.yaml`: as PrometheusRule resources
   with the Prometheus Operator, or their `spec` as plain `rule_files`.
3. Alertmanager loads `deploy/monitoring/alertmanager.yaml`: all alerts go to
   the receiver `operators` (e-mail and the NTF bridge, repeated every
   4 hours), critical ones instead to `operators-critical` (e-mail, the
   on-call webhook and the NTF bridge, hourly). `ReticoraWorkerStopped`
   suppresses `ReticoraWorkerNoSuccess` of the same worker.
4. The NTF bridge of the server (`POST /api/v1/admin/alerts/alertmanager`,
   OPS-05, E-75) turns each notification of an alert group into a
   notification of the operator organization; the `notifier` worker
   delivers it over that organization's notification channels (e-mail,
   in-app, webhook) with retry, like every other notification (NTF-04).

The file ships with placeholders of the reserved domain `.invalid`; replace
them before loading it:

| Setting | Placeholder | Set to |
|---|---|---|
| `global.smtp_smarthost` | `smtp.example.invalid:587` | Host and port of the SMTP relay |
| `global.smtp_from` | `alertmanager@example.invalid` | Sender address of the alert e-mails |
| `global.smtp_auth_username` | `alertmanager@example.invalid` | SMTP login; remove it with `smtp_auth_password_file` for a relay without authentication |
| `global.smtp_auth_password_file` | `/etc/alertmanager/secrets/smtp-password` | File with the SMTP password, for example a mounted secret; never write the password into the file |
| `email_configs[].to` | `ops@example.invalid` | Addresses of the operators and of the on-call team |
| `webhook_configs[].url` | `https://oncall.example.invalid/alertmanager` | On-call pager or chat bridge |
| `webhook_configs[].url` of the NTF bridge (anchor `ntf-bridge`) | `https://reticora.example.invalid/api/v1/admin/alerts/alertmanager` | Base URL of the Reticora server as Alertmanager reaches it (ingress or service), with the path unchanged |
| `http_config.authorization.credentials_file` of the NTF bridge | `/etc/alertmanager/secrets/ops-alert-token` | File with `RETICORA_OPS_ALERT_TOKEN`, for example a mounted secret; never write the token into the file (`credentials`) |

`global.smtp_require_tls: true` stays unless the relay offers no STARTTLS.
Check the edited file with `amtool check-config alertmanager.yaml`
(Alertmanager refuses to load a file with e-mail receivers but without an
SMTP smarthost and sender).

#### NTF bridge (OPS-05, E-75)

Set on every server instance that serves the API:

| Variable | Meaning |
|---|---|
| `RETICORA_OPS_ALERT_ORG_ID` | UUID of the operator organization, a regular organization of the operator team whose notification channels and users receive the alerts. Without it the bridge answers 404, also with a valid token, and the e-mail receivers deliver alone (transition rule of E-75) |
| `RETICORA_OPS_ALERT_TOKEN` | Bearer token of Alertmanager, at least 32 characters, not `RETICORA_OPERATOR_TOKEN`; surrounding white space (the line break of a secret file) is ignored, as Alertmanager ignores it in its credentials file. Required with `RETICORA_OPS_ALERT_ORG_ID`; a token without the organization is reported at start and leaves the bridge off |

The token is the only credential of the bridge: user sessions, API keys,
operator sessions and the break-glass token are refused with 401, and each
refusal is recorded in `operator_audit` (`POST /alerts/alertmanager`, status
401) and counts against the pre-auth limit of the client address. The token
is compared in constant time.

What the bridge does with a notification (payload version 4, at most 1 MiB
and 100 alerts; Alertmanager sends at most 50, `max_alerts`):

- It queues one notification of the category `ops_alert` on every enabled
  channel of the operator organization, for its users who hold
  `monitoring:write` organization-wide (`org_admin`, `engineer`); a webhook
  channel gets one delivery. A firing group with an alert of severity
  `critical` ignores quiet hours and muted categories; a resolved one and
  every other group respect them.
- The deduplication key is derived from the group key, the status and the
  latest `startsAt` of the group's alerts: Alertmanager's repetitions
  (`repeat_interval`) and retries queue nothing (answer `duplicate`); the
  resolution of the group and a new alert in it notify again.
- Subject (German) and text (German and English) name the alert, its labels,
  `startsAt` and the summary; label and annotation values are stripped of
  line breaks and control characters and cut to a fixed length. The values
  are never logged.
- The deliveries and the audit entry `notification.ops_alert_queued` in the
  audit log of the operator organization (status, alert name, number of
  alerts and deliveries, deduplication key; no token) are written in one
  transaction of that organization (RLS unchanged). Other organizations do
  not see them.
- It answers 503 when the operator organization has no enabled channel or no
  user with `monitoring:write`: Alertmanager reports the notification as
  failed (`alertmanager_notifications_failed_total{integration="webhook"}`),
  the e-mail of the same receiver is delivered regardless.

`TestAlertmanagerConfigDeliversToTheBridge` (`backend/internal/operator`)
checks that every receiver of the route tree keeps its e-mail and posts to
the bridge once, with resolved notifications and the token from a
credentials file; `alerts_integration_test.go` runs the chain from the
webhook to the delivery on PostgreSQL.

The CI job `monitoring-config` runs `tests/monitoring-config.test.sh` with
amtool v0.27.0 and promtool v2.54.1 (both pinned with their checksums):
`amtool check-config` of the shipped file, `promtool check rules` on the
groups of every PrometheusRule and `promtool test rules` on the unit tests in
`deploy/monitoring/tests` (`worker-rules.test.yaml`: each worker alert fires
and stays silent where it must; `operator-rules.test.yaml`). Locally:

```bash
AMTOOL=<amtool> PROMTOOL=<promtool> ./tests/monitoring-config.test.sh
```

### Break-glass alert (SEC-07)

`deploy/monitoring/operator-rules.yaml` defines `ReticoraOperatorBreakGlassUsed` (severity `critical`): it fires as soon as `reticora_operator_break_glass_total` of an instance increases, that is with the first evaluation after a request to `/api/v1/admin/*` was authenticated with `RETICORA_OPERATOR_TOKEN` (header `X-Operator-Token`), and stays active for up to 5 minutes. A series whose first sample is already above 0 (use between process start and first scrape) also fires, for 5 minutes. It goes to the on-call receiver like every critical alert. The promtool unit test is `deploy/monitoring/tests/operator-rules.test.yaml`; the CI job `gate` runs it through `tests/audit/t2-break-glass-alert.test.sh`.

After the alert:

1. Ask who used the token and why. Only bootstrap and break-glass (operator login via OIDC not possible) justify it; routine work uses a personal operator login.
2. Read the uses in `operator_audit` (`GET /api/v1/admin/audit`, operator kind `break_glass`) and check the chain with `GET /api/v1/admin/audit/verify`. The server log of the instance named in the alert has a line `SECURITY ALERT: operator break-glass token used` with method, path and remote address per request.
3. If the use is not justified or cannot be attributed, treat the token as compromised: set a new `RETICORA_OPERATOR_TOKEN` (at least 32 characters) on every server instance and restart them, then review the organizations and entitlements written in the audited requests.
4. Record the result (justification or incident) with the alert.

## Planned workers

The OPS-02 workers below do not exist yet as separate runners. The job
runner of OPS-02 is the `jobs` worker; scheduled system jobs register with
the `scheduler` instead of getting a worker of their own. Each one
gets its switch, following the convention above, in the work package that
introduces it:

| Planned name | Requirement |
|---|---|
| `audit_runner` | Audit jobs |
| `workflow` | Workflows [P3] |
| `iga` | IGA provisioning tasks [A] |

The offline detection is registered: `offline_detector` for the CIs (see
above); the offline status of the collectors is the system job
`collector.status` of the scheduler (every minute, COL-01). Until a planned
worker exists, its switch has no effect and is logged as a configuration
warning.

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
RETICORA_WORKER_NATS_EDGE_ENABLED=false
RETICORA_WORKER_CREDENTIAL_BUNDLES_ENABLED=false
RETICORA_WORKER_DISCOVERY_SCOPES_ENABLED=false
RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED=false
```

In the Compose installation each of these is read from `.env`; in
Kubernetes it is an `env` entry of the server container
(`deploy/k8s/base/deployment.yaml`).

The worker instance keeps the defaults. At startup the log shows
`worker started` or `worker disabled` for each worker.
