-- Revert migration 000031.

-- 5. Continuous aggregate name
ALTER MATERIALIZED VIEW metric_sample_1h RENAME TO metric_sample_hourly;

-- 4. Relationship-type CHECK (restore the 000025 vocabulary; rows using the new
--    names are removed first, otherwise the constraint could not be validated)
DELETE FROM ci_relationship
WHERE rel_type IN ('member_of_cluster', 'runs_on', 'mounted_in', 'uplink_to');

ALTER TABLE ci_relationship DROP CONSTRAINT IF EXISTS ci_relationship_rel_type_check;
ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_rel_type_check
    CHECK (rel_type IN (
        'connected_to', 'hosted_on', 'depends_on', 'member_of',
        'powers', 'powered_by', 'stores', 'monitors', 'backs_up'
    ));

-- 3. ip_address uniqueness
DROP INDEX IF EXISTS uq_ip_address_org_address_no_interface;
DROP INDEX IF EXISTS uq_ip_address_org_interface_address;
ALTER TABLE ip_address DROP CONSTRAINT IF EXISTS ip_address_organization_id_address_key;
ALTER TABLE ip_address ADD CONSTRAINT ip_address_organization_id_address_key
    UNIQUE (organization_id, address);

-- 2. client.external_ref
ALTER TABLE client DROP COLUMN IF EXISTS external_ref;

-- 1. organization.plan CHECK
ALTER TABLE organization DROP CONSTRAINT IF EXISTS organization_plan_check;
