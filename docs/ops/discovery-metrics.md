# Discovery metrics and alerts (DIS-08)

The collectors report every run of a discovery scope with its errors
(`collector.<org>.<id>.scopes.result` over the NATS endpoint, worker
`discovery_scopes`, see [workers.md](workers.md)). The server stores the
result as the scope's health (`last_result`, shown on the Discovery page and
in `GET /api/v1/discovery-scopes`) and counts the run and its errors in two
Prometheus counters. They live in `backend/internal/discovery/errors.go`
(`NormalizeRunResult`) and are served at `/metrics` of every server instance.
Only instances running the worker `discovery_scopes` count runs, so sum the
series over the instances.

## Metrics

| Metric | Labels | Counts |
|---|---|---|
| `reticora_discovery_scope_runs_total` | `state` | Scope runs the collectors reported, by the state of the run |
| `reticora_discovery_errors_total` | `protocol`, `cause` | Errors of those runs, one per error (at most 50 per run) |

The series carry no organization, client or collector: the scope and its
errors are on the Discovery page of the organization, with target and
message. A series appears with the first run (or error) of its label values
after the start of the instance.

### `state`

| Value | Meaning |
|---|---|
| `ok` | The run reported no error. |
| `partial` | The run reported errors and also found devices (records). |
| `failed` | The run reported errors and found nothing. |

A scope that has not run yet has the health `never_run`; it is not counted.

### `protocol`

The protocol the error occurred in, as the collector names it: one of the
plugins `sweep`, `snmp`, `ssh`, `wmi`, `ipmi`, `redfish`, `power`, `nas`,
or `upload` (the run's results could not be handed to the server), `scope`
(the scope itself, for example a scope cut at 65,536 addresses) and
`unknown` when the collector named none.

### `cause`

The collector classifies an error where it can (`auth_failed`,
`unreachable`, `upload_failed`); otherwise the server derives the cause from
the message (`ClassifyCause`). A cause the server does not know is derived
from the message as well; the collector's `scope_truncated` thus appears as
`config_error` with `protocol="scope"`.

| Value | Typical reason | What to check |
|---|---|---|
| `unreachable` | No route to the host, host down, unknown host name | Network path from the collector, the scope's target, VRF |
| `timeout` | The device did not answer in time | Firewall between collector and device, device load |
| `refused` | Connection refused or reset | Service not running on the device, wrong port |
| `auth_failed` | Wrong credentials: SNMP community, SSH or Redfish login, 401 | The scope's `credential_ids`, or the credentials of its client and the organization (E-67) |
| `tls` | Certificate or TLS handshake failure | Device certificate, TLS versions |
| `protocol_error` | Malformed or unexpected answer | Firmware of the device, plugin selection |
| `config_error` | Invalid configuration, for example SNMPv3 without authPriv, a truncated scope | Scope and credential settings |
| `upload_failed` | The collector could not upload its results; they wait in its spool | NATS edge, ingest worker |
| `unknown` | None of the above | The message on the Discovery page |

## Alerts

`deploy/monitoring/discovery-rules.yaml` is a PrometheusRule (Prometheus
Operator) with four alerts, all `severity: warning`. Like the other rule
files it is routed by `deploy/monitoring/alertmanager.yaml` and unit-tested
with promtool (`deploy/monitoring/tests/discovery-rules.test.yaml`, run by
`tests/monitoring-config.test.sh` in the CI job `monitoring-config`).
`TestDiscoveryAlertRulesCoverTheDiscoveryMetrics`
(`backend/internal/server/workers_test.go`) checks the rule file, its unit
test and this page.

| Alert | Condition | Hold | Meaning |
|---|---|---|---|
| `ReticoraDiscoveryScopeRunsFailing` | More than half of the scope runs of the last hour have `state="failed"` | 1h | Discovery finds nothing for most scopes: network, credentials or collectors are broken. |
| `ReticoraDiscoveryAuthFailures` | More than 10 errors with `cause="auth_failed"` per `protocol` in 30 minutes | 30m | The logins of a protocol fail: credentials wrong, expired or rotated on the devices. |
| `ReticoraDiscoveryUploadFailures` | Any error with `cause="upload_failed"` in 30 minutes | 30m | The collectors cannot deliver their results; their spool fills up. |
| `ReticoraDiscoveryConfigErrors` | Any error with `cause="config_error"` per `protocol` in the last hour | 1h | A scope or credential is configured wrongly, or a scope is larger than 65,536 addresses. |

Without scope runs the ratio of `ReticoraDiscoveryScopeRunsFailing` is not
defined and no alert fires; an installation without collectors stays silent.

## Where to look next

- The Discovery page lists each scope with the state of its last run and the
  errors with cause, protocol, target and message.
- `GET /api/v1/discovery-scopes/{id}` returns the same as `health`.
- The collector logs every failed probe with the target and the message.
