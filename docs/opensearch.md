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

The `opensearch` service runs `opensearchproject/opensearch:3.9.0` (pinned by
version and digest, scanned by `image-scan-thirdparty`) single-node with the
security plugin disabled (the compose network is the trust boundary, port 9200
only on loopback) and a health-checked volume. The server waits for it via
`depends_on: service_healthy` when the profile is enabled.

The image's entrypoint runs the demo installer of the security plugin unless
both `DISABLE_INSTALL_DEMO_CONFIG=true` and `DISABLE_SECURITY_PLUGIN=true` are
set; without `OPENSEARCH_INITIAL_ADMIN_PASSWORD` that installer stops the
container, and its demo certificates are never meant for operation. Compose,
the Kubernetes component and CI therefore set both switches. To run with the
security plugin, mount an own security configuration (certificates,
`config.yml`, internal users) and set `RETICORA_OPENSEARCH_USERNAME` /
`RETICORA_OPENSEARCH_PASSWORD`. The heap is set with `OPENSEARCH_JAVA_OPTS`;
OpenSearch ignores `ES_JAVA_OPTS`.

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

## Upgrade from OpenSearch 2.x to 3.x (Upd-02)

Releases up to 2026-11 shipped `opensearchproject/opensearch:2.19.7`; the
current pin is 3.9.0 (Lucene 10, bundled JDK 25). The index holds no data of
its own, so the supported path is a **rebuild**, not a snapshot restore:

1. Optionally switch the server to `RETICORA_SEARCH_BACKEND=postgres` for the
   maintenance window; search keeps working from PostgreSQL.
2. Stop OpenSearch and remove its data (`docker compose --profile opensearch
   down`, then `docker volume rm <project>_opensearchdata`; with Kubernetes
   delete the StatefulSet and the PVC `data-opensearch-0`). Nothing is lost:
   PostgreSQL holds every document, and a rollback to the previous release
   rebuilds a 2.x index the same way.
3. Deploy the new release and start OpenSearch 3.9.0 on the empty volume.
4. Start the server with `RETICORA_SEARCH_BACKEND=opensearch`: it applies the
   index template, the index is created on the first write.
5. Run `POST /api/v1/search/reindex` once per tenant (see the reindex
   runbook above) and check `GET <os>/reticora-search/_count`.

An in-place start of 3.9.0 on the 2.19.7 data volume works as well for
indexes created by 2.x (`minimum_index_compatibility_version` of 3.9.0 is
2.0.0; checked locally on 2026-11-13: document count, a nested identifier
query and new writes survive the switch). It is not the recommended path:
indexes created by 1.x are not readable by 3.x, and a 3.x data volume cannot
be opened by 2.x again. Snapshots from 2.x restore into 3.x only if their
indexes were created by 2.x; the rebuild avoids that question.

The own HTTP client of `internal/search` uses only APIs that are unchanged in
3.x (`_index_template`, `_mapping`, `_doc`, `_search` with nested queries,
inner hits, `docvalue_fields` and highlight). The integration tests
`opensearch_real_integration_test.go` and
`opensearch_real_fields_integration_test.go` run against the pinned version in
CI.

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
