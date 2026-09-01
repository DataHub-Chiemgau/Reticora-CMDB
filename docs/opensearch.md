# OpenSearch operations runbook (Epic G2)

OpenSearch is an **optional** full-text search backend. The default remains
PostgreSQL FTS over `search_document`; PostgreSQL is always the source of
truth, so the OpenSearch index can be rebuilt at any time.

## Enable OpenSearch

### Docker Compose

```bash
cd deploy/docker-compose
# .env: RETICORA_SEARCH_BACKEND=opensearch
docker compose --profile opensearch up -d
```

The `opensearch` service runs single-node with the security plugin disabled
(the compose network is the trust boundary) and a health-checked volume. The
server waits for it via `depends_on: service_healthy` when the profile is
enabled.

### Kubernetes

Include the optional component from an overlay:

```yaml
# overlays/prod/kustomization.yaml
resources:
  - ../../base        # deploy/k8s
components:
  - ../../base/components/opensearch
```

This adds the OpenSearch StatefulSet (PVC, resource limits,
readiness/liveness probes on `/_cluster/health`), a headless Service and a
patch that points the server at `http://opensearch:9200`. Store credentials
in a `reticora-opensearch` secret (`username`/`password` keys) when the
security plugin is enabled.

## Startup behavior

With `RETICORA_SEARCH_BACKEND=opensearch` the server, on every startup:

1. pings OpenSearch and exits non-zero if unreachable,
2. applies the index template idempotently
   (`PUT _index_template/<index>`), pinning an explicit mapping
   (`organization_id`, `entity_type`, `entity_id` as keyword; `title` as
   text + keyword subfield; `summary` as text; `metadata` as object;
   `updated_at` as date).

Because the template is an upsert, restarts are safe and the schema is
reproducible instead of relying on dynamic mapping guesses.

## Reindex runbook

**When to reindex:** after a database restore/PITR, after a mapping change,
after index corruption or accidental deletion, or when search results look
stale beyond the best-effort write-through.

**How:**

```bash
curl -X POST https://<host>/api/v1/search/reindex \
  -H "Authorization: ******"   # requires the search:write permission
```

- Returns `202` with the number of indexed documents.
- Tenant-scoped: only the caller's `organization_id` is rebuilt.
- Online: the hybrid backend rebuilds `search_document` from PostgreSQL and
  re-indexes into OpenSearch; reads keep working against the old documents
  until they are replaced.
- Duration: roughly linear in tenant size (bounded batches of 100 via the
  hybrid coordinator).

## Monitoring & troubleshooting

- **Index size / doc count:** `GET <os>/_cat/indices/reticora-search?v`
  and `GET <os>/reticora-search/_count`.
- **Cluster health:** `GET <os>/_cluster/health`. `yellow` on a single-node
  cluster usually means unassigned replicas — expected with
  `number_of_replicas: 0` it stays `green`; otherwise set replicas to 0.
- **Server logs:** startup fails loudly on ping/template errors; runtime
  indexing failures are logged as warnings (best-effort) and recovered by the
  next reindex.
- **Disk watermark:** OpenSearch blocks writes above the flood-stage
  watermark (default 95% disk). Free disk or raise
  `cluster.routing.allocation.disk.watermark.*`, then reindex.
- **Backend unreachable:** the server keeps serving PostgreSQL-backed reads;
  search queries to OpenSearch fail until it recovers. Fix connectivity and,
  if writes were missed, run a reindex.
- **Mapping drift:** the startup template is authoritative. If an index was
  created before the template existed, delete the index
  (`DELETE <os>/reticora-search`) and reindex — the template recreates it with
  the correct mapping.
