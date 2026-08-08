-- Migration 000031: schema corrections from the spec v1.1 compliance review
--
-- 1. organization.plan gains the spec'd enum CHECK (essential|standard|pro|enterprise)
-- 2. client gains the spec'd external_ref column
-- 3. ip_address uniqueness includes the interface component (spec: org+interface+address);
--    the old (organization_id, address) unique constraint is replaced by a partial
--    unique index covering only rows without an interface
-- 4. ci_relationship.rel_type CHECK gains the spec'd names member_of_cluster, runs_on,
--    mounted_in and uplink_to (powered_by was already added in 000025)
-- 5. The hourly continuous aggregate is renamed to the spec'd metric_sample_1h

-- ─── 1. organization.plan CHECK ─────────────────────────────────────────────────
ALTER TABLE organization DROP CONSTRAINT IF EXISTS organization_plan_check;
ALTER TABLE organization ADD CONSTRAINT organization_plan_check
    CHECK (plan IN ('essential', 'standard', 'pro', 'enterprise'));

-- ─── 2. client.external_ref ─────────────────────────────────────────────────────
ALTER TABLE client ADD COLUMN IF NOT EXISTS external_ref TEXT;

-- ─── 3. ip_address uniqueness with interface component ──────────────────────────
-- Rows with an interface are unique per (org, interface, address); rows without an
-- interface keep the previous (org, address) uniqueness.
ALTER TABLE ip_address DROP CONSTRAINT IF EXISTS ip_address_organization_id_address_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ip_address_org_interface_address
    ON ip_address(organization_id, interface_id, address)
    WHERE interface_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ip_address_org_address_no_interface
    ON ip_address(organization_id, address)
    WHERE interface_id IS NULL;

-- ─── 4. Spec relationship-type names ────────────────────────────────────────────
ALTER TABLE ci_relationship DROP CONSTRAINT IF EXISTS ci_relationship_rel_type_check;
ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_rel_type_check
    CHECK (rel_type IN (
        'connected_to', 'hosted_on', 'depends_on', 'member_of', 'member_of_cluster',
        'powers', 'powered_by', 'runs_on', 'mounted_in', 'uplink_to',
        'stores', 'monitors', 'backs_up'
    ));

-- ─── 5. Continuous aggregate rename to spec name ────────────────────────────────
ALTER MATERIALIZED VIEW metric_sample_hourly RENAME TO metric_sample_1h;
