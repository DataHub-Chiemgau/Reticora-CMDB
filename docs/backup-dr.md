# Backup & disaster recovery runbook (Epic H2)

The CMDB's source of truth is PostgreSQL (with TimescaleDB). Backup/DR is built
on two layers: **PITR** (point-in-time recovery via base backup + WAL archive)
as the primary mechanism, and **nightly logical dumps** as a portable,
version-independent safety net. Object storage (S3/MinIO, already in the stack)
holds both.

## RPO / RTO targets

| Tier    | Data                       | RPO        | RTO      |
| ------- | -------------------------- | ---------- | -------- |
| PITR    | All PostgreSQL data        | ≤ 15 min (WAL archived at least every 5 min) | ≤ 4 h |
| Logical | All PostgreSQL data        | 24 h       | ≤ 4 h    |
| Objects | Documents, exports (S3)    | versioned  | minutes  |
| Search  | OpenSearch index           | none       | minutes  |

The targets are those of NFR-05 (RPO 15 min, RTO 4 h). The OpenSearch index is **not** backed up: PostgreSQL `search_document` is the
source of truth and the index is rebuilt online with
`POST /api/v1/search/reindex` (see `docs/opensearch.md`).

## Backup chain: pgBackRest (OPS-04, E-29)

The chain runs on pgBackRest; E-29 decided for it. It has two parts:

- **WAL archiving:** PostgreSQL archives every completed WAL segment with
  `archive_command = 'pgbackrest --stanza=reticora archive-push %p'`. With
  `archive_timeout = 300`, at the latest every 5 minutes.
- **Base backups:** a full backup every day. Each backup expires the backups
  older than **35 days** together with their WAL
  (`repo1-retention-full-type=time`, `repo1-retention-full=35`; NFR-06).

The repositories are encrypted with AES-256 (`repo1-cipher-type`). The
passphrase `RETICORA_BACKUP_CIPHER_PASS` is generated once by
`install-cloud.sh` and stored in `.env`. **Keep a copy outside the host:**
without it no backup can be restored.

### Docker Compose

- `postgres` runs the image `deploy/docker-compose/postgres`: TimescaleDB
  plus pgBackRest, with WAL archiving switched on. Configuration:
  `deploy/docker-compose/postgres/pgbackrest.conf`.
- `backup` runs the same image, `reticora-backup.sh run`:
  - it checks its settings first and does not start if one is invalid:
    `RETICORA_BACKUP_HOUR` must be an hour 0..23 (`08` is 8), and the bucket
    of an S3 repository must be versioned (`verify-repo`, see below);
  - it creates the stanza, checks the archiving and takes a first full
    backup;
  - then it takes one full backup per day at `RETICORA_BACKUP_HOUR` (UTC,
    default 2);
  - it is healthy while the bucket versioning is `Enabled` and the newest
    backup is younger than 26 hours.
- Repository `repo1`: the `pgbackrest` volume on the same host.
- Offsite repository `repo2` (S3 over HTTPS, recommended): set these in
  `.env`; the installer keeps them on re-runs:

  ```
  RETICORA_BACKUP_S3_BUCKET=reticora-backup
  RETICORA_BACKUP_S3_ENDPOINT=s3.eu-central-1.amazonaws.com
  RETICORA_BACKUP_S3_REGION=eu-central-1
  RETICORA_BACKUP_S3_KEY=…
  RETICORA_BACKUP_S3_KEY_SECRET=…
  ```

  WAL and backups then go to both repositories. The bucket must have
  versioning and replication into a second EU region (see below); without
  versioning the backup service does not start.

Commands, with the environment of the container:

```bash
cd deploy/docker-compose
docker compose --env-file .env exec backup reticora-pgbackrest-env.sh pgbackrest --stanza=reticora info
docker compose --env-file .env exec backup reticora-pgbackrest-env.sh reticora-backup.sh health
docker compose --env-file .env exec backup reticora-pgbackrest-env.sh reticora-backup.sh verify-repo
```

### Kubernetes

The overlay `prod` includes two components (decision E-74, transitional
rule: option 1). `staging` includes neither and has no backup: it runs
against a database the operator provides.

- `deploy/k8s/base/components/postgres`, the database `reticora-db`: a
  StatefulSet with one replica and a PersistentVolumeClaim (50 Gi), the
  Service `reticora-db` (5432 PostgreSQL, 8432 pgBackRest TLS server) and
  the image `reticora-postgres`, the same as in Compose and in the backup
  job.
  - Container `postgres`: PostgreSQL starts with `config_file` from the
    ConfigMap `reticora-postgres`. It includes the configuration of the data
    directory and sets `archive_mode = on`,
    `archive_command = 'pgbackrest --stanza=reticora archive-push %p'` and
    `archive_timeout = 300`.
  - Container `pgbackrest`: creates the stanza as soon as the database
    accepts connections, then runs the pgBackRest TLS server
    (`pgbackrest server`). If `stanza-create` fails (repository unreachable,
    wrong key), it retries every 30 s. Until then the WAL waits in `pg_wal`.
  - The TLS server accepts only client certificates of the CA `ca.crt` with
    the common name `pgbackrest-client`, and only for the stanza `reticora`
    (`tls-server-auth`).
  - The server and the migrations connect to `reticora-db:5432` (secrets
    `reticora-db-runtime` and `reticora-db-migrate`, deploy/README.md).
- `deploy/k8s/base/components/backup`:
  - the ConfigMap `reticora-pgbackrest`. `pgbackrest.conf` is the S3
    repository (encryption, 35 days retention); the database and the job
    read it. `repo-host.conf` is read by the job only and points it at
    `reticora-db` over TLS;
  - the CronJob `reticora-pgbackrest-full`: `reticora-backup.sh
    verify-repo`, then a daily full backup over the TLS server. If the
    bucket is not versioned, the job fails without a backup;
  - the CronJob `reticora-backup` (pg_dump, see below).

Secrets (nothing secret is in a ConfigMap):

| Secret | Key | Used for |
| ------ | --- | -------- |
| `reticora-postgres` | `password` | bootstrap superuser `reticora` of the image (the migration login) |
| `reticora-pgbackrest` | `cipher-pass`, `s3-key`, `s3-key-secret` | repository: archive-push and stanza in the database, the backup job |
| | `ca.crt` | CA of both certificates, used for nothing else: every certificate it signs with the common name `pgbackrest-client` can read the database |
| | `server.crt`, `server.key` | TLS server of `reticora-db`; the certificate must name `reticora-db` (subjectAltName) |
| | `tls.crt`, `tls.key` | client certificate of the job, common name `pgbackrest-client` |

```bash
openssl req -x509 -newkey rsa:3072 -nodes -keyout ca.key -out ca.crt -days 3650 -subj "/CN=Reticora pgBackRest CA"
openssl req -new -newkey rsa:3072 -nodes -keyout server.key -out server.csr -subj "/CN=reticora-db"
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -out server.crt \
  -extfile <(printf 'subjectAltName=DNS:reticora-db,DNS:reticora-db.reticora.svc\nextendedKeyUsage=serverAuth\n')
openssl req -new -newkey rsa:3072 -nodes -keyout tls.key -out tls.csr -subj "/CN=pgbackrest-client"
openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -out tls.crt \
  -extfile <(printf 'extendedKeyUsage=clientAuth\n')
kubectl -n reticora create secret generic reticora-postgres --from-literal=password="$(openssl rand -base64 32)"
kubectl -n reticora create secret generic reticora-pgbackrest \
  --from-literal=cipher-pass="$(openssl rand -base64 48)" \
  --from-literal=s3-key=… --from-literal=s3-key-secret=… \
  --from-file=ca.crt --from-file=server.crt --from-file=server.key --from-file=tls.crt --from-file=tls.key
```

Keep `ca.key` and a copy of the cipher pass outside the cluster; without the
cipher pass no backup can be restored. The key needs, besides the object
permissions of pgBackRest, `s3:GetBucketVersioning` (`verify-repo`).

After the first rollout:

```bash
kubectl -n reticora exec reticora-db-0 -c postgres -- pgbackrest --stanza=reticora check
kubectl -n reticora exec reticora-db-0 -c postgres -- \
  psql -U reticora -d reticora -c 'SELECT archived_count, last_archived_wal, failed_count FROM pg_stat_archiver'
# The first full backup now instead of at 02:00:
kubectl -n reticora create job --from=cronjob/reticora-pgbackrest-full reticora-pgbackrest-first
```

Proof: the CI job `k8s-manifests` checks the rendered `prod` build (step
`Assert WAL archiving`: the archive settings, the StatefulSet that starts
PostgreSQL with them, the TLS server, the Service). The case `wal_archive` of
the workflow `k8s-smoke` runs both components in kind with MinIO as the
repository: `pgbackrest check`, `archived_count` after `pg_switch_wal()`, a
full backup by a job of the CronJob, and a failed backup with a wrong
cipher pass.

**Alternatives (E-74, still open).** Option 1 is a transitional rule. A
decision for another option replaces only the component `postgres`, the
assertion `Assert WAL archiving` and the kind case `wal_archive`:

- Option 2, the operator's database: remove the component `postgres` from
  the overlay. The database host archives into the same repository
  (`archive-push` with the `[global]` of `pgbackrest.conf`) and runs
  `pgbackrest server` with a certificate for its host name. Patch
  `pg1-host` and `pg1-path` in `repo-host.conf`.
- Option 3, managed PostgreSQL with the provider's PITR (E-29 option 3):
  neither the component `postgres` nor the pgBackRest CronJob. The
  provider's PITR takes their place, with the same RPO, RTO and retention.

### Object storage: versioning and replication (NFR-05)

- The server enables versioning on every bucket it uses at start
  (`reticora-documents`, `exports`, `RETICORA_S3_BUCKET`). An overwritten or
  deleted document keeps its previous versions.
- If the object store refuses, the server logs `bucket versioning could not
  be enabled`.
- Replication into a second EU region is part of the provider setup:
  - Terraform: `backup_replica_region`, output `backup_policy`. For
    `environment = "production"`, `terraform plan` fails without it or with
    the backup region as replica region (variable validation, NFR-05).
    `deploy/terraform/tests/backup.tftest.hcl` proves this and the EU
    regions with `terraform test` (CI job `terraform`, no cloud
    credentials);
  - MinIO: `mc replicate add`.

#### Bucket versioning of the pgBackRest repository (`verify-repo`)

`reticora-backup.sh verify-repo` enforces versioning for every S3
repository of pgBackRest:

- Which repositories: every `repo<n>-type=s3`, from the environment
  (`PGBACKREST_REPO<n>_*`) or from `[global]` of the configuration file
  (`PGBACKREST_CONFIG`, default `/etc/pgbackrest/pgbackrest.conf`).
  - Compose: `repo2` from `RETICORA_BACKUP_S3_*`, through
    `reticora-pgbackrest-env.sh`. Called without it while
    `RETICORA_BACKUP_S3_BUCKET` is set, the check fails instead of skipping
    the bucket.
  - Kubernetes: `repo1` of the ConfigMap.
  - Without an S3 repository there is nothing to check; it reports that and
    succeeds.
- The check: a signed `GET ?versioning` with curl (`--aws-sigv4`, the
  repository's own key) to `repo<n>-s3-endpoint`. It honours
  `s3-uri-style`, `storage-port`, `storage-ca-file`, `storage-ca-path` and
  `storage-verify-tls`.
  - `Enabled` passes.
  - Any other result fails: `Suspended`, never enabled, an S3 error (only
    its code is shown, e.g. `HTTP 403 AccessDenied`) or no connection.
  - The key and the secret go to curl on stdin. They appear neither on a
    command line nor in a message.
- The key needs `s3:GetBucketVersioning` on the bucket, in addition to the
  object permissions of pgBackRest.
- Where it runs:
  - Compose: `reticora-backup.sh run` checks at the start and does not
    start on failure. `health` checks every time, so the service turns
    unhealthy as soon as versioning is suspended.
  - Kubernetes: the CronJob runs it before every backup.

## Restore procedure (PITR)

1. Stop the server so that nothing writes during the restore.
   - If PostgreSQL still runs (logical error), first archive the current
     WAL: `docker compose exec backup reticora-pgbackrest-env.sh pgbackrest
     --stanza=reticora check`.
   - Then stop it: `docker compose stop server backup postgres`.
2. Restore into an empty data directory, up to the target time:

   ```bash
   docker compose --env-file .env run --rm --no-deps --user postgres \
     --entrypoint reticora-pgbackrest-env.sh postgres \
     sh -c 'find /var/lib/postgresql/data -mindepth 1 -delete && pgbackrest --stanza=reticora \
       --type=time --target="2026-10-12 09:30:00+00" --target-action=promote restore'
   ```

   Without `--type`/`--target`, the restore replays the whole archive. It
   follows the newest timeline, also after an earlier point in time
   restore.
3. Start PostgreSQL: `docker compose up -d postgres`. It replays the WAL up to
   the target and promotes.
4. Start the backup service again; it takes a new full backup on the new
   timeline. Then start the server.
5. Run `make migrate-up` (or `migrate ... up`) to apply any migrations newer
   than the backup.
6. Rebuild the search index per tenant: `POST /api/v1/search/reindex`.
7. Verify the audit hash chains: `go run ./cmd/audit-verify -all` (in `backend/`) checks the chain of every organization and names the first violation of each (sequence gap, broken link, hash mismatch; AUD-04); `-org <id>` checks one.

## Restore proof: RPO and RTO (WP-101, WP-445, NFR-05)

`tests/restore-pitr.sh` proves the chain with the real image. The `pitr`
job of `.github/workflows/restore-test.yml` runs it with its defaults every
night, on changes to the chain and on `workflow_dispatch`. The protocol
`restore-pitr-report.md` is kept as an artifact for 90 days and shown in
the job summary.

The test starts `postgres` and `backup` in a throwaway Compose project and
writes a workload after the first base backup. It then runs two scenarios:

1. **Logical error:** archive the current WAL, then restore to a target time
   between two workload phases. Every row before the target must be back,
   none after it.
2. **Loss of the data volume during continuous writes:** writes run 1,050 s
   (`RESTORE_WRITE_SECONDS`), longer than the RPO limit of 15 minutes and
   than three archive cycles. Then a crash and the restore of the latest
   archived state.
   - Measured RPO: last committed row minus last restored row.
   - The restore follows the timeline created by scenario 1.

Both restores measure the RTO: wipe, restore, WAL replay and promotion.
After the restore, archiving must work again (`pgbackrest check`). The job
fails when the point in time restore is not exact, the RTO exceeds
**14,400 s (4 h)** or the RPO exceeds one of two limits; the protocol line
`RPO … PASS|FAIL` names both:

- **Archive cycle:** `archive_timeout` of `docker-compose.yml` plus 60 s,
  **360 s**. The WAL is archived at the latest every `archive_timeout`; a
  larger loss means the chain archives less often than configured.
- **NFR-05:** **900 s (15 min)**.

For the run, the daily backup of the backup service is moved 12 hours
away, so that no backup archives the write window early.

**Negative self-test (job `rpo-negative`, F-130).** The default run with the
database on `archive_timeout=1200` (`RESTORE_ARCHIVE_TIMEOUT`). The write
window ends before `archive_timeout` forces an archive, so the latest
restore lacks all of it. The job expects exit 1 with the line `RPO … FAIL` above both
limits and no other failed check; if the script passes, the job fails. With
the former write window of 420 s, shorter than the limit, the RPO check
could not fail.

**Representative data volume (job `pitr-seeded`, F-292).** The same proof
with the migrations applied and the demo organization of `cmd/seed` scaled
to 10,000 CIs (`RESTORE_SEED_OBJECTS=10000`) in the base backup. Its RTO is
the statement for a realistic database; the protocol names the database
size at the first base backup. Artifact `restore-pitr-seeded-report`.

What needs no chain runs in the ci.yml job `gate`:
`tests/restore-pitr-config.test.sh` checks the write window against both
limits, the RPO line against fixture numbers, the script against stubs,
this workflow and the expectation of `rpo-negative`.

Locally: `./tests/restore-pitr.sh` (Docker Compose);
`RESTORE_ARCHIVE_TIMEOUT=1200` for the self-test;
`RESTORE_SEED_OBJECTS=10000 RESTORE_SEED_BIN=<built cmd/seed>` with
golang-migrate in `PATH` for the seeded run.

### Measured values

Measured in [restore-test run 40261526875](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40261526875)
on 2026-10-26 (`pull_request` of PR #95, head `235a7b6` with the WP-445
state, GitHub-hosted `ubuntu-24.04`); the values are copied from the
protocol lines of the job logs, nothing here is estimated. The earlier
values of 2026-10-12 (30 s of writes, toy data volume) are withdrawn
(F-292).

Default run (job `pitr`, artifact `restore-pitr-report`):

| Field | Value |
|---|---|
| Run | 40261526875, job `pitr`, 2026-10-26 02:55–03:15 UTC |
| Commit | `235a7b6` (PR #95, merge ref of the pull request) |
| Point in time restore | 50000 rows before the target, 0 after (PASS) |
| RPO | 150.2 s, 6850 of 98051 rows lost (limits 360 s and 900 s: PASS) |
| RTO point in time restore | 3 s (limit 14400 s) |
| RTO latest state | 4 s (limit 14400 s) |
| Archiving after the restore | `pgbackrest check` ok |
| Database size at the first base backup | 9 MB (restore size 34.1 MB) |

Representative data volume (job `pitr-seeded`, artifact
`restore-pitr-seeded-report`, `seed -objects 10000`):

| Field | Value |
|---|---|
| Database size at the first base backup | 56 MB (restore size 84.5 MB) |
| RTO point in time restore | 4 s (limit 14400 s) |
| RTO latest state | 4 s (limit 14400 s) |
| RPO | 150.7 s, 6900 of 98101 rows lost (PASS) |

Negative self-test (job `rpo-negative`, artifact
`restore-pitr-negative-report`, `RESTORE_ARCHIVE_TIMEOUT=1200`):

| Field | Value |
|---|---|
| Job result | passed: the script exited 1, the protocol ends **Result:** FAILED, only the RPO line fails |
| RPO | 1068.3 s, above 360 s and 900 s (47350 of 97351 rows lost) |

The RTO values hold for the data volumes of these runs on a CI runner;
they say nothing about a production volume of several GB (SR-06: dedicated
reference host before GA).

With continuous writes, the RPO is bounded by `archive_timeout` (300 s)
plus the archive lag. The RTO grows with the database size and with the WAL
to replay since the last daily full backup.

## Logical backup (portable safety net)

`deploy/k8s/base/components/backup/cronjob.yaml` runs a nightly `pg_dump` (custom format,
compressed) and uploads it to S3. It is version-independent and can be restored
into any compatible PostgreSQL:

```
pg_dump --format=custom --file=/backup/reticora-$(date +%F).dump "$DATABASE_URL"
# restore:
pg_restore --clean --if-exists --dbname="$DATABASE_URL" /backup/<file>.dump
```

## Automated restore test (CI)

`.github/workflows/restore-test.yml` runs nightly (`schedule:` cron) and proves
restorability without touching production data:

1. start TimescaleDB, apply all migrations,
2. insert fixture data (tenant, CI, audit rows),
3. `pg_dump` → restore into a fresh database,
4. integrity checks: table/row counts match, and the audit hash chain verifies
   via the `audit-verify` binary.

The job fails if the restore or any integrity check fails. It uses only
generated test data — never real credentials or tenant data. The jobs
`pitr`, `pitr-seeded` and `rpo-negative` of the same workflow prove the
pgBackRest chain (see "Restore proof: RPO and RTO" above).

## Migration 000056: quarantined references (E-26)

Migration 000056 adds composite foreign keys so that `ci_relationship` and
`composition` can only reference CIs and assets of their own organization.
Rows that violate this (dangling or cross-tenant references) block the
constraints.

**Current behaviour.** The migration moves such rows unchanged into
`migration_quarantine` (`source_table`, `row_data` as JSON, `reason`,
`migration = '000056'`). Nothing is deleted. The table has RLS per
organization; an operator reviews it with a maintenance connection:

```sql
SELECT organization_id, source_table, reason, quarantined_at, row_data
FROM migration_quarantine
ORDER BY organization_id, source_table;
```

Repair a row by re-creating the relationship or composition with valid
references through the API, then delete the quarantine entry. `migrate down`
past 000056 moves the rows that 000056 quarantined back into their tables and
then drops `migration_quarantine`. The down migrations of later migrations run
before it; entries they keep (for example those of 000166 and 000179, or the
review rows that 000095 to 000097 and 000130 park there) are dropped with the
table, so export it before such a rollback if they are still needed.

**Rollback on installations migrated before this change.** Such an
installation has no `migration_quarantine` (see the release note below).
Since WP-574 (F-224, E-72 Option 1) the down migration of 000056 checks
`to_regclass('migration_quarantine')`: without the table there is nothing to
move back, and it drops the composite keys and the rest as usual. Use the
migration files of a release that contains this change:

```bash
migrate -path backend/migrations -database "$DATABASE_URL" down 1   # from version 56
```

With the migration files of an older release the down migration stops with
`relation "migration_quarantine" does not exist` and leaves version 55 dirty
(the transaction is rolled back, the schema is still at version 56). Either
run `migrate … force 56` and repeat the rollback with the files of a current
release, or, staying with the older files, create an empty table first and
then roll back; the down migration drops it again:

```sql
SELECT to_regclass('public.migration_quarantine');  -- NULL on such an installation
CREATE TABLE migration_quarantine (source_table text NOT NULL, migration text NOT NULL, row_data jsonb NOT NULL);
```

```bash
migrate -path backend/migrations -database "$DATABASE_URL" force 56   # only if the failed attempt left 55 dirty
migrate -path backend/migrations -database "$DATABASE_URL" down 1
```

Both ways are covered: `TestMigration56DownWithoutQuarantineTable`
(`backend/internal/composition/quarantine_integration_test.go`) runs the
down section on a schema with the composite keys and without the table;
`TestMigration56QuarantinesInconsistentRows` checks that with the table the
rows come back as before.

**Release note for installations migrated before this change.** Earlier
builds of 000056 deleted those rows without a copy. An installation is
affected if it reached schema version 56 or later while `migration_quarantine`
did not exist yet (the table is only created by the corrected migration, so
it is missing there). Check from backup whether rows were lost:

1. Restore the last backup taken **before** the upgrade to version 56 into an
   isolated database (never over production), see the procedures above.
   `SELECT version FROM schema_migrations` must report a version below 56.
2. On that restored copy, list the rows the old migration would have deleted:

   ```sql
   SELECT 'ci_relationship' AS source_table, r.*
   FROM ci_relationship r
   WHERE NOT EXISTS (SELECT 1 FROM ci c WHERE c.id = r.source_ci_id AND c.organization_id = r.organization_id)
      OR NOT EXISTS (SELECT 1 FROM ci c WHERE c.id = r.target_ci_id AND c.organization_id = r.organization_id);

   SELECT 'composition' AS source_table, k.*
   FROM composition k
   WHERE NOT EXISTS (SELECT 1 FROM asset a WHERE a.id = k.parent_asset_id AND a.organization_id = k.organization_id)
      OR (k.child_ci_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ci c WHERE c.id = k.child_ci_id AND c.organization_id = k.organization_id))
      OR (k.child_asset_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM asset a WHERE a.id = k.child_asset_id AND a.organization_id = k.organization_id));
   ```

3. Empty results: nothing was lost. Otherwise export the rows (`\copy (…) TO
   'lost-000056.csv' CSV HEADER`), clarify each row with the affected tenant
   and re-create the valid ones through the API. Record the check in the
   operations log.

## Migration 000179: containment cycles (AST-05)

Migration 000089 turned stored `contains` rows into `ci.parent_ci_id` before
it created the cycle trigger, so cyclic legacy rows (A contains B, B contains
A) became a containment cycle (F-243). Migration 000179 breaks every
`parent_ci_id` cycle at its youngest CI (`created_at`): it moves that CI's
`parent_ci_id` into `migration_quarantine` (`source_table = 'ci'`,
`migration = '000179'`; `row_data` holds `id`, `parent_ci_id` and `cycle`,
the CIs from this one along their parents) and clears it. All other parents
stay. Review the entries with a maintenance connection:

```sql
SELECT organization_id, row_data->>'id' AS ci_id, row_data->>'parent_ci_id' AS old_parent,
       row_data->'cycle' AS cycle, quarantined_at
FROM migration_quarantine
WHERE migration = '000179'
ORDER BY organization_id, quarantined_at;
```

Set the correct parent through the API (the cycle trigger refuses a parent
that closes the cycle again), then delete the entry. `migrate down` past
000179 keeps the repair and the entries: a restored parent would close the
cycle again.

## Outage and backpressure suite (OPS-06)

`tests/outage/run.sh` tests the behaviour on a dependency outage against an
installed Compose stack. `.github/workflows/outage.yml` runs it every night,
before every release (`release.yml` publishes only after it passed), on
changes to the suite and by hand (`workflow_dispatch`). Before the
installation it merges Mailpit and the central SMTP server of the
installation (`RETICORA_SMTP_HOST=mailpit`) into the source-build override
(`tests/outage/smtp.sh overlay`), so `/readyz` has the `smtp` check from the
first start. The protocol `outage-report.md` and the protocol of the
negative self-test (`outage-selftest-report.md`) are kept as the workflow
artifact `outage-report` for 90 days. `tests/outage/config.test.sh` (CI job
`gate`) checks the suite without a stack: the SMTP scenario, the accounting
and the self-test against stubs, the overlay and outage.yml.

During every dependency outage `tests/outage/load.sh` sends 50 API writes
(`POST /api/v1/cis`) and 50 ingest records (`POST /api/v1/ingest/bulk`, one
record per request) with the API key of an organization seeded by
`cmd/seed` (slug prefix `outage-`). Every answer must be accepted (2xx) or
visibly refused (429 or 503). After the dependency is back the suite waits
until the ingest queue (stream `RETICORA_INGEST`) is empty and no
`event_outbox` entry that occurred before the recovery is unpublished, and
then counts in the database: the CIs with the run's serial-number prefix
must be exactly the accepted writes and records (**accepted == persisted**).
An accepted write that is missing is lost, a refused one that is stored
anyway is late; both fail the scenario, as does a server restart
(`RestartCount`). Timing and log lines decide nothing. Every count is in the
protocol.

| Scenario | Simulation | Expected |
|---|---|---|
| PostgreSQL, NATS, Redis, object storage (MinIO) | `docker compose stop <service>` for at least 30 s, with the load | `/readyz` answers 503 and reports exactly that dependency as `unavailable`, so load balancers stop routing (backpressure). The server process keeps running without restarts and is ready again after the restart. A marker row written before the outage is still there. The load: PostgreSQL down → every request 503 (the API key cannot be checked: not 401, so the pre-auth limit does not lock the client out); NATS down → CI writes accepted (the event outbox keeps their events until NATS is back), ingest refused with 503 at once and never queued later; Redis down → 429 (the rate limiter fails closed); MinIO down → accepted. Then accepted == persisted, the ingest queue is empty and the outbox published. |
| SMTP (`RETICORA_SMTP_HOST`, Mailpit) | `docker compose stop mailpit`, with the load; an invitation (a notification over the e-mail channel `central_smtp`) during the outage | `/readyz` answers 503 with `"smtp":"unavailable"`, so **a mail-server outage takes every instance out of the load balancer** (Compose healthcheck, Kubernetes readinessProbe) although the API itself still works (the load is accepted). The delivery stays `pending` with `attempts` ≥ 1 and the cause in `last_error`. The notifier retries 2 minutes after the first failure (doubling, at most 5 attempts), so Mailpit stays down at least 25 s after the failed attempt; once it is back the delivery is `sent` within 120 s and Mailpit holds exactly one message to the invitee. The server logs `readiness check failed` with `check=smtp` and the address once. Without `RETICORA_SMTP_HOST` there is no `smtp` check. |
| Collector, server unreachable | `TestUploadRecords_FailureSpoolsAndFlushDelivers`, `TestHeartbeatReportsSpoolBackpressureAndLoss` | Records go to the spool and are delivered once the server is reachable. The spool level and every loss are reported. |
| Collector spool limits | `TestDiskBufferSizeLimitAppliesBackpressure`, `TestDiskBufferMaxAgeKeepsMessagesFor24Hours`, `TestPersistenceAcrossBufferInstances` | A full spool refuses new data instead of dropping old data silently. The spool survives restarts. |
| Webhook target down | `TestDispatcherPersistsAndRetriesFailedDelivery`, `TestDispatcherMarksDeliveryFailedAfterMaxAttempts` | Deliveries are persisted and retried. After the last attempt they go to the dead-letter queue. |
| Negative self-test | `OUTAGE_SELFTEST_DROP=1 ./tests/outage/run.sh minio`: the last ingest record of the load counts as accepted but is never sent | The run fails with exactly that record lost; outage.yml fails if it passes or fails for another reason. |

A refusal for an unreachable dependency is 503 with `Retry-After`: a
request with an API key while PostgreSQL is down, an ingest while NATS is
down (nothing is queued), and a request whose database connection cannot be
established. A collector keeps such a batch in its spool and sends it again;
records of the batch that were queued before a failure are deduplicated by
`record_id` (REC-01).

A configured SMTP server is a readiness dependency like the others (OPS-01,
WP-442): while it is unreachable, all instances report not ready at the same
time, so plan relay maintenance like a database maintenance window, or remove
`RETICORA_SMTP_HOST` for the duration (self-signup and `central_smtp`
notifications are off without it). For the same reason an installation with
a configured but unreachable SMTP server fails in `wait_for_service server`;
the server log shown by the installer names the check and its address
([troubleshooting](troubleshooting-installation.md#container-docker-compose-server-1-is-unhealthy)).

Run it locally like CI: merge Mailpit into the override
(`./tests/outage/smtp.sh overlay deploy/docker-compose/docker-compose.override.yml`;
the file is rewritten without its comments, restore it afterwards), then
install the checkout (`./install.sh --cloud --non-interactive
--build-from-source`):

```bash
./tests/outage/run.sh                         # all scenarios
./tests/outage/run.sh nats redis              # selected scenarios
OUTAGE_SELFTEST_DROP=1 OUTAGE_REPORT=outage-selftest-report.md \
  ./tests/outage/run.sh minio                 # must fail: one record lost
./tests/outage/config.test.sh                 # without a stack (CI job gate)
```

## DR checklist

- [ ] Confirm the last successful base backup / WAL archive lag is within RPO.
- [ ] Restore to an isolated environment (never over production).
- [ ] Re-apply migrations; verify `schema_migrations` matches the app version.
- [ ] Rebuild search indexes (online, per tenant).
- [ ] Verify audit hash chain before opening the system to users.
- [ ] Repoint the application (DNS/ingress) and monitor the SLO alerts
      (`deploy/monitoring/slo-rules.yaml`).
