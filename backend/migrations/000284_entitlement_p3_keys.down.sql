-- Migration 000284 down: the rows added by the up migration go, the former
-- rows (workflow_forms, compliance) come back exactly from
-- migration_quarantine, and the entries of the migration are removed.

DELETE FROM entitlement e
USING migration_quarantine q
WHERE q.migration = '000284' AND q.source_table = 'entitlement'
  AND q.reason = 'ENT-06 key change (WP-862): added row'
  AND e.id = (q.row_data ->> 'id')::uuid;

-- A former row of an organization deleted since cannot come back.
INSERT INTO entitlement (id, organization_id, feature_key, plan, enabled, limits, valid_until, source,
                         created_at, updated_at)
SELECT r.id, r.organization_id, r.feature_key, r.plan, r.enabled, r.limits, r.valid_until, r.source,
       r.created_at, r.updated_at
FROM migration_quarantine q
CROSS JOIN LATERAL jsonb_populate_record(NULL::entitlement, q.row_data) AS r
WHERE q.migration = '000284' AND q.source_table = 'entitlement'
  AND q.reason = 'ENT-06 key change (WP-862): former row'
  AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id);

DELETE FROM migration_quarantine WHERE migration = '000284';
