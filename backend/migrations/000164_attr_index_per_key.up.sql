-- WP-607 (NFR-02, NFR-01; decision E-73, supplement of 2026-10-26): one
-- attribute index per key instead of one per definition and organization.
--
-- The DDL job built one partial index on ci per searchable definition,
-- with the predicate organization_id = '<org>'. On the volumes of NFR-01
-- every one of 500 organizations makes cost_center searchable, and ci
-- carries 528 instead of 28 indexes: every query on ci opens and plans all
-- of them (planning about ten times slower), every write updates or checks
-- all of them (about three times slower), ANALYZE ci computes the statistics
-- of every expression, and a generic plan never uses one of them, because
-- organization_id = $1 does not prove the predicate (load runs of WP-439,
-- cause U1).
--
-- A searchable definition of an organization that is not unique and has no
-- CI type is now served by the index of its key (citype.UsesKeyIndex):
--   idx_attr_key_<key> (idx_attr_keyh_<hash> for other keys) ON ci
--   (organization_id, reticora_attr_text(attributes, '<key>'))
--   WHERE deleted_at IS NULL AND reticora_attr_text(attributes, '<key>') IS NOT NULL
-- without a predicate on the organization. It holds the CIs of every
-- organization; the tenants stay apart by row level security (ci_isolation,
-- FORCE) and the organization the attribute filter names, and
-- reticora_attr_text stays LEAKPROOF (000162). The index goes when no
-- definition uses the key any more. Unique definitions and definitions of
-- a CI type keep the index of the definition.
--
-- The DDL job builds the indexes (CREATE INDEX CONCURRENTLY outside a
-- transaction), so this migration queues one attribute.index.key job per
-- definition the index of its key serves: the job builds the index of the
-- key unless it exists and drops the definition's own index only after it
-- is valid (citype.DDLJob.Ensure). The kind is new, so a server of the
-- previous version that still runs during a rolling update leaves the jobs
-- alone (it would end an attribute.index or attribute.index.rebuild job with
-- the index of the definition in place). A definition with a queued
-- attribute.index.key job gets no second one.

INSERT INTO job (organization_id, kind, params, max_attempts, timeout_seconds)
SELECT d.organization_id, 'attribute.index.key', jsonb_build_object('definition_id', d.id::text), 3, 3600
  FROM attribute_definition d
 WHERE d.organization_id IS NOT NULL
   AND d.searchable AND NOT d.is_unique AND d.ci_type_id IS NULL
   AND NOT EXISTS (
        SELECT 1 FROM job j
         WHERE j.organization_id = d.organization_id AND j.kind = 'attribute.index.key' AND j.status = 'queued'
           AND j.params ->> 'definition_id' = d.id::text);
