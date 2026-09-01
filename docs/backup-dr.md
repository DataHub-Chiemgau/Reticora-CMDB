# Backup & disaster recovery runbook (Epic H2)

The CMDB's source of truth is PostgreSQL (with TimescaleDB). Backup/DR is built
on two layers: **PITR** (point-in-time recovery via base backup + WAL archive)
as the primary mechanism, and **nightly logical dumps** as a portable,
version-independent safety net. Object storage (S3/MinIO, already in the stack)
holds both.

## RPO / RTO targets

| Tier    | Data                       | RPO        | RTO      |
| ------- | -------------------------- | ---------- | -------- |
| PITR    | All PostgreSQL data        | ≤ 5 min    | ≤ 1 h    |
| Logical | All PostgreSQL data        | 24 h       | ≤ 4 h    |
| Search  | OpenSearch index           | none       | minutes  |

The OpenSearch index is **not** backed up: PostgreSQL `search_document` is the
source of truth and the index is rebuilt online with
`POST /api/v1/search/reindex` (see `docs/opensearch.md`).

## PITR configuration

Use WAL archiving with a base backup. Two supported approaches:

- **Managed database** (recommended): enable the provider's PITR (continuous
  WAL archiving + point-in-time restore). Set the retention window (e.g. 7–35
  days). No in-cluster components needed.
- **Self-hosted** (WAL-G or pgBackRest): archive WAL and take periodic base
  backups to S3. Example WAL-G environment on the database pod:
  ```
  WALG_S3_PREFIX=s3://reticora-backup/wal
  AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY   # from the backup secret
  archive_command = 'wal-g wal-push %p'
  restore_command = 'wal-g wal-fetch %f %p'
  ```
  Base backup nightly: `wal-g backup-push $PGDATA`. Encrypt backups
  (`WALG_PGP_KEY` or SSE) — they contain all tenant data.

## Restore procedure (PITR)

1. Provision a fresh PostgreSQL/TimescaleDB instance (same major version).
2. Restore the latest base backup, then replay WAL to the target time:
   `wal-g backup-fetch $PGDATA LATEST` and set
   `recovery_target_time` / `restore_command` before starting.
3. Run `make migrate-up` (or `migrate ... up`) to apply any migrations newer
   than the backup.
4. Rebuild the search index per tenant: `POST /api/v1/search/reindex`.
5. Verify the audit hash chain: `backend/cmd/audit-verify` (see below).

## Logical backup (portable safety net)

`deploy/k8s/backup-cronjob.yaml` runs a nightly `pg_dump` (custom format,
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

## DR checklist

- [ ] Confirm the last successful base backup / WAL archive lag is within RPO.
- [ ] Restore to an isolated environment (never over production).
- [ ] Re-apply migrations; verify `schema_migrations` matches the app version.
- [ ] Rebuild search indexes (online, per tenant).
- [ ] Verify audit hash chain before opening the system to users.
- [ ] Repoint the application (DNS/ingress) and monitor the SLO alerts
      (`deploy/monitoring/slo-rules.yaml`).
