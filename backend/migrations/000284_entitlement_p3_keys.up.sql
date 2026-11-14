-- Migration 000284: entitlement keys of phase 3 after ENT-06 and PKG-08
-- (WP-862, E-151, E-152, decision of the client of 2026-11-09).
--
-- The former module keys give way to the names of ENT-06: workflow_forms →
-- workflows, compliance → compliance_findings, with plan, enabled, limits,
-- valid_until and source of the former row (trial rows with source
-- selfsignup included). Every organization holding at least one entitlement
-- row then gets the phase-3 keys of its plan it still lacks (enabled, no
-- limits, source manual): from standard on monitoring and ipam; from pro on
-- also monitoring_network, credential_vault, notifications_sms_pager,
-- workflows and compliance_findings; enterprise also monitoring_access,
-- monitoring_analytics and compliance_automation. The add-on admp is part of
-- no plan (CH14). Existing rows stay as they are; the missing rows of an
-- organization without rows come from Provision at the next start (WP-470).
--
-- The former rows are kept in migration_quarantine (reason 'ENT-06 key
-- change (WP-862): former row'), every row added here is noted there as well
-- (reason 'ENT-06 key change (WP-862): added row'), so the down migration
-- restores the former rows exactly.

-- (0) Keep the former rows.
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT e.organization_id, 'entitlement', to_jsonb(e), 'ENT-06 key change (WP-862): former row', '000284'
FROM entitlement e
WHERE e.feature_key IN ('workflow_forms', 'compliance');

-- (1) Successors with the fields of the former row.
WITH added AS (
    INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source)
    SELECT e.organization_id, k.successor, e.plan, e.enabled, e.limits, e.valid_until, e.source
    FROM entitlement e
    JOIN (VALUES ('workflow_forms', 'workflows'),
                 ('compliance', 'compliance_findings')) AS k (former, successor) ON k.former = e.feature_key
    ON CONFLICT (organization_id, feature_key) DO NOTHING
    RETURNING *
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT added.organization_id, 'entitlement', to_jsonb(added), 'ENT-06 key change (WP-862): added row', '000284'
FROM added;

-- (2) The missing phase-3 keys of the plan (E-151) for every organization
-- with entitlement rows. Tier 2 is from standard, 3 from pro, 4 enterprise.
WITH added AS (
    INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, source)
    SELECT o.id, k.feature_key, o.plan, true, '{}'::jsonb, 'manual'
    FROM organization o
    JOIN (VALUES ('monitoring', 2), ('ipam', 2),
                 ('monitoring_network', 3), ('credential_vault', 3), ('notifications_sms_pager', 3),
                 ('workflows', 3), ('compliance_findings', 3),
                 ('monitoring_access', 4), ('monitoring_analytics', 4), ('compliance_automation', 4)) AS k (feature_key, tier)
        ON k.tier <= CASE o.plan WHEN 'standard' THEN 2 WHEN 'pro' THEN 3 WHEN 'enterprise' THEN 4 ELSE 1 END
    WHERE EXISTS (SELECT 1 FROM entitlement e WHERE e.organization_id = o.id)
    ON CONFLICT (organization_id, feature_key) DO NOTHING
    RETURNING *
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT added.organization_id, 'entitlement', to_jsonb(added), 'ENT-06 key change (WP-862): added row', '000284'
FROM added;

-- (3) The former rows go.
DELETE FROM entitlement WHERE feature_key IN ('workflow_forms', 'compliance');
