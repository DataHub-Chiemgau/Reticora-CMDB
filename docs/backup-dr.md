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
  - it creates the stanza, checks the archiving and takes a first full
    backup;
  - then it takes one full backup per day at `RETICORA_BACKUP_HOUR` (UTC,
    default 2);
  - it is healthy while the newest backup is younger than 26 hours.
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
  versioning and replication into a second EU region (see below).

Commands, with the environment of the container:

```bash
cd deploy/docker-compose
docker compose --env-file .env exec backup reticora-pgbackrest-env.sh pgbackrest --stanza=reticora info
docker compose --env-file .env exec backup reticora-pgbackrest-env.sh reticora-backup.sh health
```

### Kubernetes

The component `deploy/k8s/base/components/backup` contains:

- the `reticora-pgbackrest` ConfigMap: S3 repository, encryption, 35 days
  retention;
- the CronJob `reticora-pgbackrest-full`: a daily full backup over the
  pgBackRest TLS connection to the database host.

On the database host, PostgreSQL archives with the same repository
configuration and runs `pgbackrest server`. Secret: `reticora-pgbackrest`
(cipher pass, S3 keys, TLS client certificate). With a managed database, its
PITR takes the place of pgBackRest, with the same RPO, RTO and retention.

### Object storage: versioning and replication (NFR-05)

- The server enables versioning on every bucket it uses at start
  (`reticora-documents`, `exports`, `RETICORA_S3_BUCKET`). An overwritten or
  deleted document keeps its previous versions.
- If the object store refuses, the server logs `bucket versioning could not
  be enabled`.
- Replication into a second EU region is part of the provider setup:
  - Terraform: `backup_replica_region`, output `backup_policy`; production
    requires it;
  - MinIO: `mc replicate add`.

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

## Restore proof: RPO and RTO (WP-101, NFR-05)

`tests/restore-pitr.sh` proves the chain with the real image. The `pitr`
job of `.github/workflows/restore-test.yml` runs it every night and on
changes to the chain. The protocol `restore-pitr-report.md` is kept as an
artifact for 90 days and shown in the job summary.

The test starts `postgres` and `backup` in a throwaway Compose project and
writes a workload after the first base backup. It then runs two scenarios:

1. **Logical error:** archive the current WAL, then restore to a target time
   between two workload phases. Every row before the target must be back,
   none after it.
2. **Loss of the data volume during continuous writes:** writes run 7
   minutes, longer than `archive_timeout`, so that a full archive cycle is
   covered. Then a crash and the restore of the latest archived state.
   - Measured RPO: last committed row minus last restored row.
   - The restore follows the timeline created by scenario 1.

Both restores measure the RTO: wipe, restore, WAL replay and promotion.
After the restore, archiving must work again (`pgbackrest check`). The job
fails above **RPO 900 s (15 min)** or **RTO 14,400 s (4 h)**, or when the
point in time restore is not exact.

Measured on 2026-10-12 with a 50,000 row workload and 30 s of writes:

| Check | Result |
|---|---|
| Point in time restore | exact (50,000 rows before the target, none after it) |
| RPO | 52 s |
| RTO | 5 s per restore |
| Archiving after the restore | works |

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
generated test data — never real credentials or tenant data.

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
past 000056 moves all quarantined rows back into their tables.

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

## Outage and backpressure suite (OPS-06)

`tests/outage/run.sh` tests the behaviour on a dependency outage against an
installed Compose stack. `.github/workflows/outage.yml` runs it every night,
before every release (`release.yml` publishes only after it passed) and on
changes to the suite. The protocol `outage-report.md` is kept as a workflow
artifact for 90 days.

| Scenario | Simulation | Expected |
|---|---|---|
| PostgreSQL, NATS, Redis, object storage (MinIO) | `docker compose stop <service>` for 30 s | `/readyz` answers 503 and reports exactly that dependency as `unavailable`, so load balancers stop routing (backpressure). The server process keeps running without restarts and is ready again after the restart. A marker row written before the outage is still there. |
| Collector, server unreachable | `TestUploadRecords_FailureSpoolsAndFlushDelivers`, `TestHeartbeatReportsSpoolBackpressureAndLoss` | Records go to the spool and are delivered once the server is reachable. The spool level and every loss are reported. |
| Collector spool limits | `TestDiskBufferSizeLimitAppliesBackpressure`, `TestDiskBufferMaxAgeKeepsMessagesFor24Hours`, `TestPersistenceAcrossBufferInstances` | A full spool refuses new data instead of dropping old data silently. The spool survives restarts. |
| Webhook target down | `TestDispatcherPersistsAndRetriesFailedDelivery`, `TestDispatcherMarksDeliveryFailedAfterMaxAttempts` | Deliveries are persisted and retried. After the last attempt they go to the dead-letter queue. |
| SMTP | — | Not applicable while no SMTP delivery exists (TEC-13). It becomes a scenario with the notification channel. |

Run it locally after `./install.sh --cloud --non-interactive`:

```bash
./tests/outage/run.sh            # all scenarios
./tests/outage/run.sh nats redis # selected scenarios
```

## DR checklist

- [ ] Confirm the last successful base backup / WAL archive lag is within RPO.
- [ ] Restore to an isolated environment (never over production).
- [ ] Re-apply migrations; verify `schema_migrations` matches the app version.
- [ ] Rebuild search indexes (online, per tenant).
- [ ] Verify audit hash chain before opening the system to users.
- [ ] Repoint the application (DNS/ingress) and monitor the SLO alerts
      (`deploy/monitoring/slo-rules.yaml`).
