-- Migration 000287: the references of the core, location, network, rack,
-- role and discovery tables stay in their organization (WP-876, SR-03 rest I;
-- TEN-09, E-137, E-167).
--
-- PostgreSQL checks foreign keys without row level security: a reference
-- that names the referenced row only (REFERENCES … (id)) accepts the id of an
-- object of another organization (SR-03). Migrations 000136, 000194 (WP-838)
-- and 000275 (WP-827) made the references of clients and of the phase-2
-- modules composite. The 59 references below, on 37 tables, become composite
-- foreign keys (column, organization_id) REFERENCES … (id, organization_id):
-- a reference into another organization fails like an unknown id (23503).
-- Constraint names and ON DELETE actions stay (SET NULL as SET NULL
-- (column), pattern 000136), so the error mapping of the repositories is
-- unchanged. pii_subject is referenced by its surrogate_id.
--
-- Every target gets UNIQUE (<column>, organization_id) where it is missing
-- (Konvention 19): a DO block adds <table>_<column>_organization_key only if
-- no unique constraint over exactly these columns exists.
--
-- Existing rows whose reference crosses the organization are not deleted
-- (E-26, pattern 000194): a nullable reference is recorded in
-- migration_quarantine with the whole row and set to NULL, a row with a
-- mandatory reference moves to migration_quarantine (its dependent rows
-- follow their own ON DELETE actions). A specialist row of the location tree
-- (building, room, rack, site) shares its id with its node; a crossing one
-- can only come from a write that bypassed the triggers, so the migration
-- stops with the list instead of changing the tree (pattern 000177). The
-- down migration gives the rows back. The remaining references without
-- organization are listed in rls.ReferencesWithoutOrganization (WP-877).

DO $$
DECLARE
    crossing text;
BEGIN
    SELECT string_agg(c.r, ', ' ORDER BY c.r) INTO crossing FROM (
        SELECT 'building ' || t.id FROM building t JOIN location x ON x.id = t.id WHERE x.organization_id <> t.organization_id
        UNION ALL
        SELECT 'rack ' || t.id FROM rack t JOIN location x ON x.id = t.id WHERE x.organization_id <> t.organization_id
        UNION ALL
        SELECT 'room ' || t.id FROM room t JOIN location x ON x.id = t.id WHERE x.organization_id <> t.organization_id
        UNION ALL
        SELECT 'site ' || t.id FROM site t JOIN location x ON x.id = t.id WHERE x.organization_id <> t.organization_id
    ) AS c(r);
    IF crossing IS NOT NULL THEN
        RAISE EXCEPTION 'location specialist rows of another organization than their node, correct them before migration 000287: %', crossing;
    END IF;
END
$$;

-- Every crossing reference is recorded before the first change.
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'agent_enrollment_token', to_jsonb(t), 'SR-03 (WP-876): agent_enrollment_token.site_id references another organization, set to NULL', '000287'
  FROM agent_enrollment_token t JOIN site x ON x.id = t.site_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'api_key', to_jsonb(t), 'SR-03 (WP-876): api_key.rotated_from references another organization, set to NULL', '000287'
  FROM api_key t JOIN api_key x ON x.id = t.rotated_from WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'cable', to_jsonb(t), 'SR-03 (WP-876): cable.source_interface_id references another organization, set to NULL', '000287'
  FROM cable t JOIN network_interface x ON x.id = t.source_interface_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'cable', to_jsonb(t), 'SR-03 (WP-876): cable.target_interface_id references another organization, set to NULL', '000287'
  FROM cable t JOIN network_interface x ON x.id = t.target_interface_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ci', to_jsonb(t), 'SR-03 (WP-876): ci.room_id references another organization, set to NULL', '000287'
  FROM ci t JOIN room x ON x.id = t.room_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ci', to_jsonb(t), 'SR-03 (WP-876): ci.site_id references another organization, set to NULL', '000287'
  FROM ci t JOIN site x ON x.id = t.site_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ci_merge', to_jsonb(t), 'SR-03 (WP-876): ci_merge.site_id references another organization, set to NULL', '000287'
  FROM ci_merge t JOIN site x ON x.id = t.site_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ci_relationship', to_jsonb(t), 'SR-03 (WP-876): ci_relationship.source_interface_id references another organization, set to NULL', '000287'
  FROM ci_relationship t JOIN network_interface x ON x.id = t.source_interface_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ci_relationship', to_jsonb(t), 'SR-03 (WP-876): ci_relationship.target_interface_id references another organization, set to NULL', '000287'
  FROM ci_relationship t JOIN network_interface x ON x.id = t.target_interface_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ci_relationship', to_jsonb(t), 'SR-03 (WP-876): ci_relationship.verified_by references another organization, set to NULL', '000287'
  FROM ci_relationship t JOIN app_user x ON x.id = t.verified_by WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'collector', to_jsonb(t), 'SR-03 (WP-876): collector.site_id references another organization, set to NULL', '000287'
  FROM collector t JOIN site x ON x.id = t.site_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'collector_enrollment_code', to_jsonb(t), 'SR-03 (WP-876): collector_enrollment_code.used_by_collector_id references another organization, set to NULL', '000287'
  FROM collector_enrollment_code t JOIN collector x ON x.id = t.used_by_collector_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'contact', to_jsonb(t), 'SR-03 (WP-876): contact.pii_subject_id references another organization, set to NULL', '000287'
  FROM contact t JOIN pii_subject x ON x.surrogate_id = t.pii_subject_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'discovery_result', to_jsonb(t), 'SR-03 (WP-876): discovery_result.matched_ci_id references another organization, set to NULL', '000287'
  FROM discovery_result t JOIN ci x ON x.id = t.matched_ci_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'discovery_scope', to_jsonb(t), 'SR-03 (WP-876): discovery_scope.site_id references another organization, set to NULL', '000287'
  FROM discovery_scope t JOIN site x ON x.id = t.site_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'endpoint_agent', to_jsonb(t), 'SR-03 (WP-876): endpoint_agent.ci_id references another organization, set to NULL', '000287'
  FROM endpoint_agent t JOIN ci x ON x.id = t.ci_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ip_address', to_jsonb(t), 'SR-03 (WP-876): ip_address.interface_id references another organization, set to NULL', '000287'
  FROM ip_address t JOIN network_interface x ON x.id = t.interface_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ip_address', to_jsonb(t), 'SR-03 (WP-876): ip_address.subnet_id references another organization, set to NULL', '000287'
  FROM ip_address t JOIN subnet x ON x.id = t.subnet_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'pii_subject', to_jsonb(t), 'SR-03 (WP-876): pii_subject.parent_id references another organization, set to NULL', '000287'
  FROM pii_subject t JOIN pii_subject x ON x.surrogate_id = t.parent_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'role_assignment', to_jsonb(t), 'SR-03 (WP-876): role_assignment.scope_site_id references another organization, set to NULL', '000287'
  FROM role_assignment t JOIN site x ON x.id = t.scope_site_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'role_permission', to_jsonb(t), 'SR-03 (WP-876): role_permission.granted_by references another organization, set to NULL', '000287'
  FROM role_permission t JOIN app_user x ON x.id = t.granted_by WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'service_account_role', to_jsonb(t), 'SR-03 (WP-876): service_account_role.scope_site_id references another organization, set to NULL', '000287'
  FROM service_account_role t JOIN site x ON x.id = t.scope_site_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'subnet', to_jsonb(t), 'SR-03 (WP-876): subnet.site_id references another organization, set to NULL', '000287'
  FROM subnet t JOIN site x ON x.id = t.site_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'team', to_jsonb(t), 'SR-03 (WP-876): team.lead_id references another organization, set to NULL', '000287'
  FROM team t JOIN app_user x ON x.id = t.lead_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'user_custom_role', to_jsonb(t), 'SR-03 (WP-876): user_custom_role.granted_by references another organization, set to NULL', '000287'
  FROM user_custom_role t JOIN app_user x ON x.id = t.granted_by WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'vlan', to_jsonb(t), 'SR-03 (WP-876): vlan.site_id references another organization, set to NULL', '000287'
  FROM vlan t JOIN site x ON x.id = t.site_id WHERE x.organization_id <> t.organization_id;

-- The nullable references are cleared through the regular triggers,
-- which derive the scope columns again from what is left.
UPDATE agent_enrollment_token t SET site_id = NULL
  FROM site x WHERE x.id = t.site_id AND x.organization_id <> t.organization_id;
UPDATE api_key t SET rotated_from = NULL
  FROM api_key x WHERE x.id = t.rotated_from AND x.organization_id <> t.organization_id;
UPDATE cable t SET source_interface_id = NULL
  FROM network_interface x WHERE x.id = t.source_interface_id AND x.organization_id <> t.organization_id;
UPDATE cable t SET target_interface_id = NULL
  FROM network_interface x WHERE x.id = t.target_interface_id AND x.organization_id <> t.organization_id;
UPDATE ci t SET room_id = NULL
  FROM room x WHERE x.id = t.room_id AND x.organization_id <> t.organization_id;
UPDATE ci t SET site_id = NULL
  FROM site x WHERE x.id = t.site_id AND x.organization_id <> t.organization_id;
UPDATE ci_merge t SET site_id = NULL
  FROM site x WHERE x.id = t.site_id AND x.organization_id <> t.organization_id;
UPDATE ci_relationship t SET source_interface_id = NULL
  FROM network_interface x WHERE x.id = t.source_interface_id AND x.organization_id <> t.organization_id;
UPDATE ci_relationship t SET target_interface_id = NULL
  FROM network_interface x WHERE x.id = t.target_interface_id AND x.organization_id <> t.organization_id;
UPDATE ci_relationship t SET verified_by = NULL
  FROM app_user x WHERE x.id = t.verified_by AND x.organization_id <> t.organization_id;
UPDATE collector t SET site_id = NULL
  FROM site x WHERE x.id = t.site_id AND x.organization_id <> t.organization_id;
UPDATE collector_enrollment_code t SET used_by_collector_id = NULL
  FROM collector x WHERE x.id = t.used_by_collector_id AND x.organization_id <> t.organization_id;
UPDATE contact t SET pii_subject_id = NULL
  FROM pii_subject x WHERE x.surrogate_id = t.pii_subject_id AND x.organization_id <> t.organization_id;
UPDATE discovery_result t SET matched_ci_id = NULL
  FROM ci x WHERE x.id = t.matched_ci_id AND x.organization_id <> t.organization_id;
UPDATE discovery_scope t SET site_id = NULL
  FROM site x WHERE x.id = t.site_id AND x.organization_id <> t.organization_id;
UPDATE endpoint_agent t SET ci_id = NULL
  FROM ci x WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id;
UPDATE ip_address t SET interface_id = NULL
  FROM network_interface x WHERE x.id = t.interface_id AND x.organization_id <> t.organization_id;
UPDATE ip_address t SET subnet_id = NULL
  FROM subnet x WHERE x.id = t.subnet_id AND x.organization_id <> t.organization_id;
UPDATE pii_subject t SET parent_id = NULL
  FROM pii_subject x WHERE x.surrogate_id = t.parent_id AND x.organization_id <> t.organization_id;
UPDATE role_assignment t SET scope_site_id = NULL
  FROM site x WHERE x.id = t.scope_site_id AND x.organization_id <> t.organization_id;
UPDATE role_permission t SET granted_by = NULL
  FROM app_user x WHERE x.id = t.granted_by AND x.organization_id <> t.organization_id;
UPDATE service_account_role t SET scope_site_id = NULL
  FROM site x WHERE x.id = t.scope_site_id AND x.organization_id <> t.organization_id;
UPDATE subnet t SET site_id = NULL
  FROM site x WHERE x.id = t.site_id AND x.organization_id <> t.organization_id;
UPDATE team t SET lead_id = NULL
  FROM app_user x WHERE x.id = t.lead_id AND x.organization_id <> t.organization_id;
UPDATE user_custom_role t SET granted_by = NULL
  FROM app_user x WHERE x.id = t.granted_by AND x.organization_id <> t.organization_id;
UPDATE vlan t SET site_id = NULL
  FROM site x WHERE x.id = t.site_id AND x.organization_id <> t.organization_id;

-- A row whose mandatory reference crosses the organization moves to
-- migration_quarantine.
WITH moved AS (
    DELETE FROM asset_location t
     USING asset x
     WHERE x.id = t.asset_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'asset_location', to_jsonb(moved), 'SR-03 (WP-876): asset_location.asset_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM building t
     USING site x
     WHERE x.id = t.site_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'building', to_jsonb(moved), 'SR-03 (WP-876): building.site_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM ci_change t
     USING ci x
     WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_change', to_jsonb(moved), 'SR-03 (WP-876): ci_change.ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM ci_contact t
     USING ci x
     WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_contact', to_jsonb(moved), 'SR-03 (WP-876): ci_contact.ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM ci_contact t
     USING contact x
     WHERE x.id = t.contact_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_contact', to_jsonb(moved), 'SR-03 (WP-876): ci_contact.contact_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM ci_field_value t
     USING ci x
     WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_field_value', to_jsonb(moved), 'SR-03 (WP-876): ci_field_value.ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM ci_instance_field_definition t
     USING ci x
     WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_instance_field_definition', to_jsonb(moved), 'SR-03 (WP-876): ci_instance_field_definition.ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM ci_relationship t
     USING ci x
     WHERE x.id = t.source_ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_relationship', to_jsonb(moved), 'SR-03 (WP-876): ci_relationship.source_ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM ci_relationship t
     USING ci x
     WHERE x.id = t.target_ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_relationship', to_jsonb(moved), 'SR-03 (WP-876): ci_relationship.target_ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM collector_certificate t
     USING collector x
     WHERE x.id = t.collector_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'collector_certificate', to_jsonb(moved), 'SR-03 (WP-876): collector_certificate.collector_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM discovery_job t
     USING collector x
     WHERE x.id = t.collector_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'discovery_job', to_jsonb(moved), 'SR-03 (WP-876): discovery_job.collector_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM discovery_result t
     USING discovery_job x
     WHERE x.id = t.job_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'discovery_result', to_jsonb(moved), 'SR-03 (WP-876): discovery_result.job_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM discovery_scope t
     USING collector x
     WHERE x.id = t.collector_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'discovery_scope', to_jsonb(moved), 'SR-03 (WP-876): discovery_scope.collector_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM network_interface t
     USING ci x
     WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'network_interface', to_jsonb(moved), 'SR-03 (WP-876): network_interface.ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM rack t
     USING room x
     WHERE x.id = t.room_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'rack', to_jsonb(moved), 'SR-03 (WP-876): rack.room_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM rack_mount t
     USING ci x
     WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'rack_mount', to_jsonb(moved), 'SR-03 (WP-876): rack_mount.ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM rack_mount t
     USING rack x
     WHERE x.id = t.rack_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'rack_mount', to_jsonb(moved), 'SR-03 (WP-876): rack_mount.rack_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM relationship_suppression t
     USING ci x
     WHERE x.id = t.source_ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'relationship_suppression', to_jsonb(moved), 'SR-03 (WP-876): relationship_suppression.source_ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM relationship_suppression t
     USING ci x
     WHERE x.id = t.target_ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'relationship_suppression', to_jsonb(moved), 'SR-03 (WP-876): relationship_suppression.target_ci_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM role_assignment t
     USING role x
     WHERE x.id = t.role_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'role_assignment', to_jsonb(moved), 'SR-03 (WP-876): role_assignment.role_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM role_assignment t
     USING app_user x
     WHERE x.id = t.user_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'role_assignment', to_jsonb(moved), 'SR-03 (WP-876): role_assignment.user_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM role_permission t
     USING role x
     WHERE x.id = t.role_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'role_permission', to_jsonb(moved), 'SR-03 (WP-876): role_permission.role_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM room t
     USING building x
     WHERE x.id = t.building_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'room', to_jsonb(moved), 'SR-03 (WP-876): room.building_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM service_account_role t
     USING role x
     WHERE x.id = t.role_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'service_account_role', to_jsonb(moved), 'SR-03 (WP-876): service_account_role.role_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM team_member t
     USING team x
     WHERE x.id = t.team_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'team_member', to_jsonb(moved), 'SR-03 (WP-876): team_member.team_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM team_member t
     USING app_user x
     WHERE x.id = t.user_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'team_member', to_jsonb(moved), 'SR-03 (WP-876): team_member.user_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM user_custom_role t
     USING custom_role x
     WHERE x.id = t.custom_role_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'user_custom_role', to_jsonb(moved), 'SR-03 (WP-876): user_custom_role.custom_role_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM user_custom_role t
     USING app_user x
     WHERE x.id = t.user_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'user_custom_role', to_jsonb(moved), 'SR-03 (WP-876): user_custom_role.user_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;
WITH moved AS (
    DELETE FROM user_invitation t
     USING role x
     WHERE x.id = t.role_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'user_invitation', to_jsonb(moved), 'SR-03 (WP-876): user_invitation.role_id references another organization, moved to migration_quarantine', '000287'
  FROM moved;

-- UNIQUE (<column>, organization_id) on the targets that lack it.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'api_key'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE api_key ADD CONSTRAINT api_key_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'network_interface'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE network_interface ADD CONSTRAINT network_interface_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'collector'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE collector ADD CONSTRAINT collector_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'pii_subject'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['organization_id', 'surrogate_id']
    ) THEN
        ALTER TABLE pii_subject ADD CONSTRAINT pii_subject_surrogate_id_organization_key UNIQUE (surrogate_id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'discovery_job'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE discovery_job ADD CONSTRAINT discovery_job_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'subnet'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE subnet ADD CONSTRAINT subnet_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'rack'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE rack ADD CONSTRAINT rack_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'building'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE building ADD CONSTRAINT building_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

ALTER TABLE agent_enrollment_token
    DROP CONSTRAINT agent_enrollment_token_site_id_fkey,
    ADD CONSTRAINT agent_enrollment_token_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES site (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE api_key
    DROP CONSTRAINT api_key_rotated_from_fkey,
    ADD CONSTRAINT api_key_rotated_from_fkey FOREIGN KEY (rotated_from, organization_id) REFERENCES api_key (id, organization_id) ON DELETE SET NULL (rotated_from);
ALTER TABLE asset_location
    DROP CONSTRAINT asset_location_asset_id_fkey,
    ADD CONSTRAINT asset_location_asset_id_fkey FOREIGN KEY (asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE CASCADE;
ALTER TABLE building
    DROP CONSTRAINT building_location_fkey,
    ADD CONSTRAINT building_location_fkey FOREIGN KEY (id, organization_id) REFERENCES location (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT building_site_id_fkey,
    ADD CONSTRAINT building_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES site (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE cable
    DROP CONSTRAINT cable_source_interface_id_fkey,
    ADD CONSTRAINT cable_source_interface_id_fkey FOREIGN KEY (source_interface_id, organization_id) REFERENCES network_interface (id, organization_id) ON DELETE SET NULL (source_interface_id),
    DROP CONSTRAINT cable_target_interface_id_fkey,
    ADD CONSTRAINT cable_target_interface_id_fkey FOREIGN KEY (target_interface_id, organization_id) REFERENCES network_interface (id, organization_id) ON DELETE SET NULL (target_interface_id);
ALTER TABLE ci
    DROP CONSTRAINT ci_room_id_fkey,
    ADD CONSTRAINT ci_room_id_fkey FOREIGN KEY (room_id, organization_id) REFERENCES room (id, organization_id),
    DROP CONSTRAINT ci_site_id_fkey,
    ADD CONSTRAINT ci_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES site (id, organization_id);
ALTER TABLE ci_change
    DROP CONSTRAINT ci_change_ci_id_fkey,
    ADD CONSTRAINT ci_change_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE ci_contact
    DROP CONSTRAINT ci_contact_ci_id_fkey,
    ADD CONSTRAINT ci_contact_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT ci_contact_contact_id_fkey,
    ADD CONSTRAINT ci_contact_contact_id_fkey FOREIGN KEY (contact_id, organization_id) REFERENCES contact (id, organization_id) ON DELETE CASCADE;
ALTER TABLE ci_field_value
    DROP CONSTRAINT ci_field_value_ci_id_fkey,
    ADD CONSTRAINT ci_field_value_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE ci_instance_field_definition
    DROP CONSTRAINT ci_instance_field_definition_ci_id_fkey,
    ADD CONSTRAINT ci_instance_field_definition_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE ci_merge
    DROP CONSTRAINT ci_merge_site_id_fkey,
    ADD CONSTRAINT ci_merge_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES site (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE ci_relationship
    DROP CONSTRAINT ci_relationship_source_ci_id_fkey,
    ADD CONSTRAINT ci_relationship_source_ci_id_fkey FOREIGN KEY (source_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT ci_relationship_source_interface_id_fkey,
    ADD CONSTRAINT ci_relationship_source_interface_id_fkey FOREIGN KEY (source_interface_id, organization_id) REFERENCES network_interface (id, organization_id),
    DROP CONSTRAINT ci_relationship_target_ci_id_fkey,
    ADD CONSTRAINT ci_relationship_target_ci_id_fkey FOREIGN KEY (target_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT ci_relationship_target_interface_id_fkey,
    ADD CONSTRAINT ci_relationship_target_interface_id_fkey FOREIGN KEY (target_interface_id, organization_id) REFERENCES network_interface (id, organization_id),
    DROP CONSTRAINT ci_relationship_verified_by_fkey,
    ADD CONSTRAINT ci_relationship_verified_by_fkey FOREIGN KEY (verified_by, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (verified_by);
ALTER TABLE collector
    DROP CONSTRAINT collector_site_id_fkey,
    ADD CONSTRAINT collector_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES site (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE collector_certificate
    DROP CONSTRAINT collector_certificate_collector_id_fkey,
    ADD CONSTRAINT collector_certificate_collector_id_fkey FOREIGN KEY (collector_id, organization_id) REFERENCES collector (id, organization_id) ON DELETE CASCADE;
ALTER TABLE collector_enrollment_code
    DROP CONSTRAINT collector_enrollment_code_used_by_collector_id_fkey,
    ADD CONSTRAINT collector_enrollment_code_used_by_collector_id_fkey FOREIGN KEY (used_by_collector_id, organization_id) REFERENCES collector (id, organization_id) ON DELETE SET NULL (used_by_collector_id);
ALTER TABLE contact
    DROP CONSTRAINT contact_pii_subject_id_fkey,
    ADD CONSTRAINT contact_pii_subject_id_fkey FOREIGN KEY (pii_subject_id, organization_id) REFERENCES pii_subject (surrogate_id, organization_id) ON DELETE SET NULL (pii_subject_id);
ALTER TABLE discovery_job
    DROP CONSTRAINT discovery_job_collector_id_fkey,
    ADD CONSTRAINT discovery_job_collector_id_fkey FOREIGN KEY (collector_id, organization_id) REFERENCES collector (id, organization_id) ON DELETE CASCADE;
ALTER TABLE discovery_result
    DROP CONSTRAINT discovery_result_job_id_fkey,
    ADD CONSTRAINT discovery_result_job_id_fkey FOREIGN KEY (job_id, organization_id) REFERENCES discovery_job (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT discovery_result_matched_ci_id_fkey,
    ADD CONSTRAINT discovery_result_matched_ci_id_fkey FOREIGN KEY (matched_ci_id, organization_id) REFERENCES ci (id, organization_id);
ALTER TABLE discovery_scope
    DROP CONSTRAINT discovery_scope_collector_id_fkey,
    ADD CONSTRAINT discovery_scope_collector_id_fkey FOREIGN KEY (collector_id, organization_id) REFERENCES collector (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT discovery_scope_site_id_fkey,
    ADD CONSTRAINT discovery_scope_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES site (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE endpoint_agent
    DROP CONSTRAINT endpoint_agent_ci_id_fkey,
    ADD CONSTRAINT endpoint_agent_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id);
ALTER TABLE ip_address
    DROP CONSTRAINT ip_address_interface_id_fkey,
    ADD CONSTRAINT ip_address_interface_id_fkey FOREIGN KEY (interface_id, organization_id) REFERENCES network_interface (id, organization_id) ON DELETE SET NULL (interface_id),
    DROP CONSTRAINT ip_address_subnet_id_fkey,
    ADD CONSTRAINT ip_address_subnet_id_fkey FOREIGN KEY (subnet_id, organization_id) REFERENCES subnet (id, organization_id) ON DELETE SET NULL (subnet_id);
ALTER TABLE network_interface
    DROP CONSTRAINT network_interface_ci_id_fkey,
    ADD CONSTRAINT network_interface_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE pii_subject
    DROP CONSTRAINT pii_subject_parent_id_fkey,
    ADD CONSTRAINT pii_subject_parent_id_fkey FOREIGN KEY (parent_id, organization_id) REFERENCES pii_subject (surrogate_id, organization_id) ON DELETE CASCADE;
ALTER TABLE rack
    DROP CONSTRAINT rack_location_fkey,
    ADD CONSTRAINT rack_location_fkey FOREIGN KEY (id, organization_id) REFERENCES location (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT rack_room_id_fkey,
    ADD CONSTRAINT rack_room_id_fkey FOREIGN KEY (room_id, organization_id) REFERENCES room (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE rack_mount
    DROP CONSTRAINT rack_mount_ci_id_fkey,
    ADD CONSTRAINT rack_mount_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT rack_mount_rack_id_fkey,
    ADD CONSTRAINT rack_mount_rack_id_fkey FOREIGN KEY (rack_id, organization_id) REFERENCES rack (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE relationship_suppression
    DROP CONSTRAINT relationship_suppression_source_ci_id_fkey,
    ADD CONSTRAINT relationship_suppression_source_ci_id_fkey FOREIGN KEY (source_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT relationship_suppression_target_ci_id_fkey,
    ADD CONSTRAINT relationship_suppression_target_ci_id_fkey FOREIGN KEY (target_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE role_assignment
    DROP CONSTRAINT role_assignment_role_id_fkey,
    ADD CONSTRAINT role_assignment_role_id_fkey FOREIGN KEY (role_id, organization_id) REFERENCES role (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT role_assignment_scope_site_id_fkey,
    ADD CONSTRAINT role_assignment_scope_site_id_fkey FOREIGN KEY (scope_site_id, organization_id) REFERENCES site (id, organization_id),
    DROP CONSTRAINT role_assignment_user_id_fkey,
    ADD CONSTRAINT role_assignment_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE;
ALTER TABLE role_permission
    DROP CONSTRAINT role_permission_granted_by_fkey,
    ADD CONSTRAINT role_permission_granted_by_fkey FOREIGN KEY (granted_by, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (granted_by),
    DROP CONSTRAINT role_permission_role_id_fkey,
    ADD CONSTRAINT role_permission_role_id_fkey FOREIGN KEY (role_id, organization_id) REFERENCES role (id, organization_id) ON DELETE CASCADE;
ALTER TABLE room
    DROP CONSTRAINT room_building_id_fkey,
    ADD CONSTRAINT room_building_id_fkey FOREIGN KEY (building_id, organization_id) REFERENCES building (id, organization_id) ON DELETE RESTRICT,
    DROP CONSTRAINT room_location_fkey,
    ADD CONSTRAINT room_location_fkey FOREIGN KEY (id, organization_id) REFERENCES location (id, organization_id) ON DELETE CASCADE;
ALTER TABLE service_account_role
    DROP CONSTRAINT service_account_role_role_id_fkey,
    ADD CONSTRAINT service_account_role_role_id_fkey FOREIGN KEY (role_id, organization_id) REFERENCES role (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT service_account_role_scope_site_id_fkey,
    ADD CONSTRAINT service_account_role_scope_site_id_fkey FOREIGN KEY (scope_site_id, organization_id) REFERENCES site (id, organization_id);
ALTER TABLE site
    DROP CONSTRAINT site_location_fkey,
    ADD CONSTRAINT site_location_fkey FOREIGN KEY (id, organization_id) REFERENCES location (id, organization_id) ON DELETE CASCADE;
ALTER TABLE subnet
    DROP CONSTRAINT subnet_site_id_fkey,
    ADD CONSTRAINT subnet_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES site (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE team
    DROP CONSTRAINT team_lead_id_fkey,
    ADD CONSTRAINT team_lead_id_fkey FOREIGN KEY (lead_id, organization_id) REFERENCES app_user (id, organization_id);
ALTER TABLE team_member
    DROP CONSTRAINT team_member_team_id_fkey,
    ADD CONSTRAINT team_member_team_id_fkey FOREIGN KEY (team_id, organization_id) REFERENCES team (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT team_member_user_id_fkey,
    ADD CONSTRAINT team_member_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE;
ALTER TABLE user_custom_role
    DROP CONSTRAINT user_custom_role_custom_role_id_fkey,
    ADD CONSTRAINT user_custom_role_custom_role_id_fkey FOREIGN KEY (custom_role_id, organization_id) REFERENCES custom_role (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT user_custom_role_granted_by_fkey,
    ADD CONSTRAINT user_custom_role_granted_by_fkey FOREIGN KEY (granted_by, organization_id) REFERENCES app_user (id, organization_id),
    DROP CONSTRAINT user_custom_role_user_id_fkey,
    ADD CONSTRAINT user_custom_role_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE;
ALTER TABLE user_invitation
    DROP CONSTRAINT user_invitation_role_id_fkey,
    ADD CONSTRAINT user_invitation_role_id_fkey FOREIGN KEY (role_id, organization_id) REFERENCES role (id, organization_id) ON DELETE CASCADE;
ALTER TABLE vlan
    DROP CONSTRAINT vlan_site_id_fkey,
    ADD CONSTRAINT vlan_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES site (id, organization_id) ON DELETE RESTRICT;
