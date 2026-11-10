-- Reverts WP-876: the 59 references name the referenced row only again,
-- the unique keys the up migration may have added go where no foreign key
-- uses them any more, and the rows the up migration changed come back from
-- migration_quarantine.

ALTER TABLE agent_enrollment_token
    DROP CONSTRAINT agent_enrollment_token_site_id_fkey,
    ADD CONSTRAINT agent_enrollment_token_site_id_fkey FOREIGN KEY (site_id) REFERENCES site (id) ON DELETE RESTRICT;
ALTER TABLE api_key
    DROP CONSTRAINT api_key_rotated_from_fkey,
    ADD CONSTRAINT api_key_rotated_from_fkey FOREIGN KEY (rotated_from) REFERENCES api_key (id) ON DELETE SET NULL;
ALTER TABLE asset_location
    DROP CONSTRAINT asset_location_asset_id_fkey,
    ADD CONSTRAINT asset_location_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES asset (id) ON DELETE CASCADE;
ALTER TABLE building
    DROP CONSTRAINT building_location_fkey,
    ADD CONSTRAINT building_location_fkey FOREIGN KEY (id) REFERENCES location (id) ON DELETE CASCADE,
    DROP CONSTRAINT building_site_id_fkey,
    ADD CONSTRAINT building_site_id_fkey FOREIGN KEY (site_id) REFERENCES site (id) ON DELETE RESTRICT;
ALTER TABLE cable
    DROP CONSTRAINT cable_source_interface_id_fkey,
    ADD CONSTRAINT cable_source_interface_id_fkey FOREIGN KEY (source_interface_id) REFERENCES network_interface (id) ON DELETE SET NULL,
    DROP CONSTRAINT cable_target_interface_id_fkey,
    ADD CONSTRAINT cable_target_interface_id_fkey FOREIGN KEY (target_interface_id) REFERENCES network_interface (id) ON DELETE SET NULL;
ALTER TABLE ci
    DROP CONSTRAINT ci_room_id_fkey,
    ADD CONSTRAINT ci_room_id_fkey FOREIGN KEY (room_id) REFERENCES room (id),
    DROP CONSTRAINT ci_site_id_fkey,
    ADD CONSTRAINT ci_site_id_fkey FOREIGN KEY (site_id) REFERENCES site (id);
ALTER TABLE ci_change
    DROP CONSTRAINT ci_change_ci_id_fkey,
    ADD CONSTRAINT ci_change_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
ALTER TABLE ci_contact
    DROP CONSTRAINT ci_contact_ci_id_fkey,
    ADD CONSTRAINT ci_contact_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE,
    DROP CONSTRAINT ci_contact_contact_id_fkey,
    ADD CONSTRAINT ci_contact_contact_id_fkey FOREIGN KEY (contact_id) REFERENCES contact (id) ON DELETE CASCADE;
ALTER TABLE ci_field_value
    DROP CONSTRAINT ci_field_value_ci_id_fkey,
    ADD CONSTRAINT ci_field_value_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
ALTER TABLE ci_instance_field_definition
    DROP CONSTRAINT ci_instance_field_definition_ci_id_fkey,
    ADD CONSTRAINT ci_instance_field_definition_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
ALTER TABLE ci_merge
    DROP CONSTRAINT ci_merge_site_id_fkey,
    ADD CONSTRAINT ci_merge_site_id_fkey FOREIGN KEY (site_id) REFERENCES site (id) ON DELETE RESTRICT;
ALTER TABLE ci_relationship
    DROP CONSTRAINT ci_relationship_source_ci_id_fkey,
    ADD CONSTRAINT ci_relationship_source_ci_id_fkey FOREIGN KEY (source_ci_id) REFERENCES ci (id) ON DELETE CASCADE,
    DROP CONSTRAINT ci_relationship_source_interface_id_fkey,
    ADD CONSTRAINT ci_relationship_source_interface_id_fkey FOREIGN KEY (source_interface_id) REFERENCES network_interface (id),
    DROP CONSTRAINT ci_relationship_target_ci_id_fkey,
    ADD CONSTRAINT ci_relationship_target_ci_id_fkey FOREIGN KEY (target_ci_id) REFERENCES ci (id) ON DELETE CASCADE,
    DROP CONSTRAINT ci_relationship_target_interface_id_fkey,
    ADD CONSTRAINT ci_relationship_target_interface_id_fkey FOREIGN KEY (target_interface_id) REFERENCES network_interface (id),
    DROP CONSTRAINT ci_relationship_verified_by_fkey,
    ADD CONSTRAINT ci_relationship_verified_by_fkey FOREIGN KEY (verified_by) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE collector
    DROP CONSTRAINT collector_site_id_fkey,
    ADD CONSTRAINT collector_site_id_fkey FOREIGN KEY (site_id) REFERENCES site (id) ON DELETE RESTRICT;
ALTER TABLE collector_certificate
    DROP CONSTRAINT collector_certificate_collector_id_fkey,
    ADD CONSTRAINT collector_certificate_collector_id_fkey FOREIGN KEY (collector_id) REFERENCES collector (id) ON DELETE CASCADE;
ALTER TABLE collector_enrollment_code
    DROP CONSTRAINT collector_enrollment_code_used_by_collector_id_fkey,
    ADD CONSTRAINT collector_enrollment_code_used_by_collector_id_fkey FOREIGN KEY (used_by_collector_id) REFERENCES collector (id) ON DELETE SET NULL;
ALTER TABLE contact
    DROP CONSTRAINT contact_pii_subject_id_fkey,
    ADD CONSTRAINT contact_pii_subject_id_fkey FOREIGN KEY (pii_subject_id) REFERENCES pii_subject (surrogate_id) ON DELETE SET NULL;
ALTER TABLE discovery_job
    DROP CONSTRAINT discovery_job_collector_id_fkey,
    ADD CONSTRAINT discovery_job_collector_id_fkey FOREIGN KEY (collector_id) REFERENCES collector (id) ON DELETE CASCADE;
ALTER TABLE discovery_result
    DROP CONSTRAINT discovery_result_job_id_fkey,
    ADD CONSTRAINT discovery_result_job_id_fkey FOREIGN KEY (job_id) REFERENCES discovery_job (id) ON DELETE CASCADE,
    DROP CONSTRAINT discovery_result_matched_ci_id_fkey,
    ADD CONSTRAINT discovery_result_matched_ci_id_fkey FOREIGN KEY (matched_ci_id) REFERENCES ci (id);
ALTER TABLE discovery_scope
    DROP CONSTRAINT discovery_scope_collector_id_fkey,
    ADD CONSTRAINT discovery_scope_collector_id_fkey FOREIGN KEY (collector_id) REFERENCES collector (id) ON DELETE CASCADE,
    DROP CONSTRAINT discovery_scope_site_id_fkey,
    ADD CONSTRAINT discovery_scope_site_id_fkey FOREIGN KEY (site_id) REFERENCES site (id) ON DELETE RESTRICT;
ALTER TABLE endpoint_agent
    DROP CONSTRAINT endpoint_agent_ci_id_fkey,
    ADD CONSTRAINT endpoint_agent_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE SET NULL;
ALTER TABLE ip_address
    DROP CONSTRAINT ip_address_interface_id_fkey,
    ADD CONSTRAINT ip_address_interface_id_fkey FOREIGN KEY (interface_id) REFERENCES network_interface (id) ON DELETE SET NULL,
    DROP CONSTRAINT ip_address_subnet_id_fkey,
    ADD CONSTRAINT ip_address_subnet_id_fkey FOREIGN KEY (subnet_id) REFERENCES subnet (id) ON DELETE SET NULL;
ALTER TABLE network_interface
    DROP CONSTRAINT network_interface_ci_id_fkey,
    ADD CONSTRAINT network_interface_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
ALTER TABLE pii_subject
    DROP CONSTRAINT pii_subject_parent_id_fkey,
    ADD CONSTRAINT pii_subject_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES pii_subject (surrogate_id) ON DELETE CASCADE;
ALTER TABLE rack
    DROP CONSTRAINT rack_location_fkey,
    ADD CONSTRAINT rack_location_fkey FOREIGN KEY (id) REFERENCES location (id) ON DELETE CASCADE,
    DROP CONSTRAINT rack_room_id_fkey,
    ADD CONSTRAINT rack_room_id_fkey FOREIGN KEY (room_id) REFERENCES room (id) ON DELETE RESTRICT;
ALTER TABLE rack_mount
    DROP CONSTRAINT rack_mount_ci_id_fkey,
    ADD CONSTRAINT rack_mount_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE,
    DROP CONSTRAINT rack_mount_rack_id_fkey,
    ADD CONSTRAINT rack_mount_rack_id_fkey FOREIGN KEY (rack_id) REFERENCES rack (id) ON DELETE RESTRICT;
ALTER TABLE relationship_suppression
    DROP CONSTRAINT relationship_suppression_source_ci_id_fkey,
    ADD CONSTRAINT relationship_suppression_source_ci_id_fkey FOREIGN KEY (source_ci_id) REFERENCES ci (id) ON DELETE CASCADE,
    DROP CONSTRAINT relationship_suppression_target_ci_id_fkey,
    ADD CONSTRAINT relationship_suppression_target_ci_id_fkey FOREIGN KEY (target_ci_id) REFERENCES ci (id) ON DELETE CASCADE;
ALTER TABLE role_assignment
    DROP CONSTRAINT role_assignment_role_id_fkey,
    ADD CONSTRAINT role_assignment_role_id_fkey FOREIGN KEY (role_id) REFERENCES role (id) ON DELETE CASCADE,
    DROP CONSTRAINT role_assignment_scope_site_id_fkey,
    ADD CONSTRAINT role_assignment_scope_site_id_fkey FOREIGN KEY (scope_site_id) REFERENCES site (id),
    DROP CONSTRAINT role_assignment_user_id_fkey,
    ADD CONSTRAINT role_assignment_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;
ALTER TABLE role_permission
    DROP CONSTRAINT role_permission_granted_by_fkey,
    ADD CONSTRAINT role_permission_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES app_user (id) ON DELETE SET NULL,
    DROP CONSTRAINT role_permission_role_id_fkey,
    ADD CONSTRAINT role_permission_role_id_fkey FOREIGN KEY (role_id) REFERENCES role (id) ON DELETE CASCADE;
ALTER TABLE room
    DROP CONSTRAINT room_building_id_fkey,
    ADD CONSTRAINT room_building_id_fkey FOREIGN KEY (building_id) REFERENCES building (id) ON DELETE RESTRICT,
    DROP CONSTRAINT room_location_fkey,
    ADD CONSTRAINT room_location_fkey FOREIGN KEY (id) REFERENCES location (id) ON DELETE CASCADE;
ALTER TABLE service_account_role
    DROP CONSTRAINT service_account_role_role_id_fkey,
    ADD CONSTRAINT service_account_role_role_id_fkey FOREIGN KEY (role_id) REFERENCES role (id) ON DELETE CASCADE,
    DROP CONSTRAINT service_account_role_scope_site_id_fkey,
    ADD CONSTRAINT service_account_role_scope_site_id_fkey FOREIGN KEY (scope_site_id) REFERENCES site (id);
ALTER TABLE site
    DROP CONSTRAINT site_location_fkey,
    ADD CONSTRAINT site_location_fkey FOREIGN KEY (id) REFERENCES location (id) ON DELETE CASCADE;
ALTER TABLE subnet
    DROP CONSTRAINT subnet_site_id_fkey,
    ADD CONSTRAINT subnet_site_id_fkey FOREIGN KEY (site_id) REFERENCES site (id) ON DELETE RESTRICT;
ALTER TABLE team
    DROP CONSTRAINT team_lead_id_fkey,
    ADD CONSTRAINT team_lead_id_fkey FOREIGN KEY (lead_id) REFERENCES app_user (id);
ALTER TABLE team_member
    DROP CONSTRAINT team_member_team_id_fkey,
    ADD CONSTRAINT team_member_team_id_fkey FOREIGN KEY (team_id) REFERENCES team (id) ON DELETE CASCADE,
    DROP CONSTRAINT team_member_user_id_fkey,
    ADD CONSTRAINT team_member_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;
ALTER TABLE user_custom_role
    DROP CONSTRAINT user_custom_role_custom_role_id_fkey,
    ADD CONSTRAINT user_custom_role_custom_role_id_fkey FOREIGN KEY (custom_role_id) REFERENCES custom_role (id) ON DELETE CASCADE,
    DROP CONSTRAINT user_custom_role_granted_by_fkey,
    ADD CONSTRAINT user_custom_role_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES app_user (id),
    DROP CONSTRAINT user_custom_role_user_id_fkey,
    ADD CONSTRAINT user_custom_role_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;
ALTER TABLE user_invitation
    DROP CONSTRAINT user_invitation_role_id_fkey,
    ADD CONSTRAINT user_invitation_role_id_fkey FOREIGN KEY (role_id) REFERENCES role (id) ON DELETE CASCADE;
ALTER TABLE vlan
    DROP CONSTRAINT vlan_site_id_fkey,
    ADD CONSTRAINT vlan_site_id_fkey FOREIGN KEY (site_id) REFERENCES site (id) ON DELETE RESTRICT;

-- A unique key the up migration added for the composite keys goes when no
-- foreign key references it any more (a later migration may use it).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'api_key'::regclass AND conname = 'api_key_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'api_key'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE api_key DROP CONSTRAINT api_key_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'network_interface'::regclass AND conname = 'network_interface_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'network_interface'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE network_interface DROP CONSTRAINT network_interface_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'collector'::regclass AND conname = 'collector_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'collector'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE collector DROP CONSTRAINT collector_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'pii_subject'::regclass AND conname = 'pii_subject_surrogate_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'pii_subject'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['surrogate_id', 'organization_id']
    ) THEN
        ALTER TABLE pii_subject DROP CONSTRAINT pii_subject_surrogate_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'discovery_job'::regclass AND conname = 'discovery_job_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'discovery_job'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE discovery_job DROP CONSTRAINT discovery_job_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'subnet'::regclass AND conname = 'subnet_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'subnet'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE subnet DROP CONSTRAINT subnet_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'rack'::regclass AND conname = 'rack_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'rack'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE rack DROP CONSTRAINT rack_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'building'::regclass AND conname = 'building_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'building'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE building DROP CONSTRAINT building_id_organization_key;
    END IF;
END
$$;

-- The recorded rows come back as they were. The triggers of the tables
-- would refuse or rewrite a reference into another organization, so they
-- are off while the rows are restored; the foreign keys stay checked. A
-- reference whose target has been deleted since stays NULL, a row whose
-- organization or target has been deleted since does not come back.
ALTER TABLE agent_enrollment_token DISABLE TRIGGER USER;
ALTER TABLE api_key DISABLE TRIGGER USER;
ALTER TABLE asset_location DISABLE TRIGGER USER;
ALTER TABLE building DISABLE TRIGGER USER;
ALTER TABLE cable DISABLE TRIGGER USER;
ALTER TABLE ci DISABLE TRIGGER USER;
ALTER TABLE ci_change DISABLE TRIGGER USER;
ALTER TABLE ci_contact DISABLE TRIGGER USER;
ALTER TABLE ci_field_value DISABLE TRIGGER USER;
ALTER TABLE ci_instance_field_definition DISABLE TRIGGER USER;
ALTER TABLE ci_merge DISABLE TRIGGER USER;
ALTER TABLE ci_relationship DISABLE TRIGGER USER;
ALTER TABLE collector DISABLE TRIGGER USER;
ALTER TABLE collector_certificate DISABLE TRIGGER USER;
ALTER TABLE collector_enrollment_code DISABLE TRIGGER USER;
ALTER TABLE contact DISABLE TRIGGER USER;
ALTER TABLE discovery_job DISABLE TRIGGER USER;
ALTER TABLE discovery_result DISABLE TRIGGER USER;
ALTER TABLE discovery_scope DISABLE TRIGGER USER;
ALTER TABLE endpoint_agent DISABLE TRIGGER USER;
ALTER TABLE ip_address DISABLE TRIGGER USER;
ALTER TABLE network_interface DISABLE TRIGGER USER;
ALTER TABLE pii_subject DISABLE TRIGGER USER;
ALTER TABLE rack DISABLE TRIGGER USER;
ALTER TABLE rack_mount DISABLE TRIGGER USER;
ALTER TABLE relationship_suppression DISABLE TRIGGER USER;
ALTER TABLE role_assignment DISABLE TRIGGER USER;
ALTER TABLE role_permission DISABLE TRIGGER USER;
ALTER TABLE room DISABLE TRIGGER USER;
ALTER TABLE service_account_role DISABLE TRIGGER USER;
ALTER TABLE subnet DISABLE TRIGGER USER;
ALTER TABLE team DISABLE TRIGGER USER;
ALTER TABLE team_member DISABLE TRIGGER USER;
ALTER TABLE user_custom_role DISABLE TRIGGER USER;
ALTER TABLE user_invitation DISABLE TRIGGER USER;
ALTER TABLE vlan DISABLE TRIGGER USER;
UPDATE agent_enrollment_token t SET site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'agent_enrollment_token'
   AND q.reason = 'SR-03 (WP-876): agent_enrollment_token.site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'site_id')::uuid);
UPDATE api_key t SET rotated_from = (q.row_data ->> 'rotated_from')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'api_key'
   AND q.reason = 'SR-03 (WP-876): api_key.rotated_from references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM api_key x WHERE x.id = (q.row_data ->> 'rotated_from')::uuid);
UPDATE cable t SET source_interface_id = (q.row_data ->> 'source_interface_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'cable'
   AND q.reason = 'SR-03 (WP-876): cable.source_interface_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM network_interface x WHERE x.id = (q.row_data ->> 'source_interface_id')::uuid);
UPDATE cable t SET target_interface_id = (q.row_data ->> 'target_interface_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'cable'
   AND q.reason = 'SR-03 (WP-876): cable.target_interface_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM network_interface x WHERE x.id = (q.row_data ->> 'target_interface_id')::uuid);
UPDATE ci t SET room_id = (q.row_data ->> 'room_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'ci'
   AND q.reason = 'SR-03 (WP-876): ci.room_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM room x WHERE x.id = (q.row_data ->> 'room_id')::uuid);
UPDATE ci t SET site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'ci'
   AND q.reason = 'SR-03 (WP-876): ci.site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'site_id')::uuid);
UPDATE ci_merge t SET site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'ci_merge'
   AND q.reason = 'SR-03 (WP-876): ci_merge.site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'site_id')::uuid);
UPDATE ci_relationship t SET source_interface_id = (q.row_data ->> 'source_interface_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'ci_relationship'
   AND q.reason = 'SR-03 (WP-876): ci_relationship.source_interface_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM network_interface x WHERE x.id = (q.row_data ->> 'source_interface_id')::uuid);
UPDATE ci_relationship t SET target_interface_id = (q.row_data ->> 'target_interface_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'ci_relationship'
   AND q.reason = 'SR-03 (WP-876): ci_relationship.target_interface_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM network_interface x WHERE x.id = (q.row_data ->> 'target_interface_id')::uuid);
UPDATE ci_relationship t SET verified_by = (q.row_data ->> 'verified_by')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'ci_relationship'
   AND q.reason = 'SR-03 (WP-876): ci_relationship.verified_by references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'verified_by')::uuid);
UPDATE collector t SET site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'collector'
   AND q.reason = 'SR-03 (WP-876): collector.site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'site_id')::uuid);
UPDATE collector_enrollment_code t SET used_by_collector_id = (q.row_data ->> 'used_by_collector_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'collector_enrollment_code'
   AND q.reason = 'SR-03 (WP-876): collector_enrollment_code.used_by_collector_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM collector x WHERE x.id = (q.row_data ->> 'used_by_collector_id')::uuid);
UPDATE contact t SET pii_subject_id = (q.row_data ->> 'pii_subject_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'contact'
   AND q.reason = 'SR-03 (WP-876): contact.pii_subject_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM pii_subject x WHERE x.surrogate_id = (q.row_data ->> 'pii_subject_id')::uuid);
UPDATE discovery_result t SET matched_ci_id = (q.row_data ->> 'matched_ci_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'discovery_result'
   AND q.reason = 'SR-03 (WP-876): discovery_result.matched_ci_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = (q.row_data ->> 'matched_ci_id')::uuid);
UPDATE discovery_scope t SET site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'discovery_scope'
   AND q.reason = 'SR-03 (WP-876): discovery_scope.site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'site_id')::uuid);
UPDATE endpoint_agent t SET ci_id = (q.row_data ->> 'ci_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'endpoint_agent'
   AND q.reason = 'SR-03 (WP-876): endpoint_agent.ci_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = (q.row_data ->> 'ci_id')::uuid);
UPDATE ip_address t SET interface_id = (q.row_data ->> 'interface_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'ip_address'
   AND q.reason = 'SR-03 (WP-876): ip_address.interface_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM network_interface x WHERE x.id = (q.row_data ->> 'interface_id')::uuid);
UPDATE ip_address t SET subnet_id = (q.row_data ->> 'subnet_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'ip_address'
   AND q.reason = 'SR-03 (WP-876): ip_address.subnet_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM subnet x WHERE x.id = (q.row_data ->> 'subnet_id')::uuid);
UPDATE pii_subject t SET parent_id = (q.row_data ->> 'parent_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'pii_subject'
   AND q.reason = 'SR-03 (WP-876): pii_subject.parent_id references another organization, set to NULL'
   AND t.surrogate_id::text = q.row_data ->> 'surrogate_id'
   AND EXISTS (SELECT 1 FROM pii_subject x WHERE x.surrogate_id = (q.row_data ->> 'parent_id')::uuid);
UPDATE role_assignment t SET scope_site_id = (q.row_data ->> 'scope_site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'role_assignment'
   AND q.reason = 'SR-03 (WP-876): role_assignment.scope_site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'scope_site_id')::uuid);
UPDATE role_permission t SET granted_by = (q.row_data ->> 'granted_by')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'role_permission'
   AND q.reason = 'SR-03 (WP-876): role_permission.granted_by references another organization, set to NULL'
   AND t.organization_id::text = q.row_data ->> 'organization_id' AND t.role_id::text = q.row_data ->> 'role_id' AND t.permission_key::text = q.row_data ->> 'permission_key'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'granted_by')::uuid);
UPDATE service_account_role t SET scope_site_id = (q.row_data ->> 'scope_site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'service_account_role'
   AND q.reason = 'SR-03 (WP-876): service_account_role.scope_site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'scope_site_id')::uuid);
UPDATE subnet t SET site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'subnet'
   AND q.reason = 'SR-03 (WP-876): subnet.site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'site_id')::uuid);
UPDATE team t SET lead_id = (q.row_data ->> 'lead_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'team'
   AND q.reason = 'SR-03 (WP-876): team.lead_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'lead_id')::uuid);
UPDATE user_custom_role t SET granted_by = (q.row_data ->> 'granted_by')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'user_custom_role'
   AND q.reason = 'SR-03 (WP-876): user_custom_role.granted_by references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'granted_by')::uuid);
UPDATE vlan t SET site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000287' AND q.source_table = 'vlan'
   AND q.reason = 'SR-03 (WP-876): vlan.site_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = (q.row_data ->> 'site_id')::uuid);
INSERT INTO asset_location
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::asset_location, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'asset_location'
   AND q.reason = 'SR-03 (WP-876): asset_location.asset_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM asset x WHERE x.id = r.asset_id)
   AND NOT EXISTS (SELECT 1 FROM asset_location e WHERE e.id = r.id);
INSERT INTO building
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::building, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'building'
   AND q.reason = 'SR-03 (WP-876): building.site_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM site x WHERE x.id = r.site_id)
   AND NOT EXISTS (SELECT 1 FROM building e WHERE e.id = r.id);
INSERT INTO ci_change
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ci_change, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'ci_change'
   AND q.reason = 'SR-03 (WP-876): ci_change.ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.ci_id)
   AND NOT EXISTS (SELECT 1 FROM ci_change e WHERE e.id = r.id);
INSERT INTO ci_contact
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ci_contact, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'ci_contact'
   AND q.reason = 'SR-03 (WP-876): ci_contact.ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.ci_id)
   AND NOT EXISTS (SELECT 1 FROM ci_contact e WHERE e.id = r.id);
INSERT INTO ci_contact
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ci_contact, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'ci_contact'
   AND q.reason = 'SR-03 (WP-876): ci_contact.contact_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM contact x WHERE x.id = r.contact_id)
   AND NOT EXISTS (SELECT 1 FROM ci_contact e WHERE e.id = r.id);
INSERT INTO ci_field_value
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ci_field_value, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'ci_field_value'
   AND q.reason = 'SR-03 (WP-876): ci_field_value.ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.ci_id)
   AND NOT EXISTS (SELECT 1 FROM ci_field_value e WHERE e.id = r.id);
INSERT INTO ci_instance_field_definition
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ci_instance_field_definition, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'ci_instance_field_definition'
   AND q.reason = 'SR-03 (WP-876): ci_instance_field_definition.ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.ci_id)
   AND NOT EXISTS (SELECT 1 FROM ci_instance_field_definition e WHERE e.id = r.id);
INSERT INTO ci_relationship
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ci_relationship, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'ci_relationship'
   AND q.reason = 'SR-03 (WP-876): ci_relationship.source_ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.source_ci_id)
   AND NOT EXISTS (SELECT 1 FROM ci_relationship e WHERE e.id = r.id);
INSERT INTO ci_relationship
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ci_relationship, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'ci_relationship'
   AND q.reason = 'SR-03 (WP-876): ci_relationship.target_ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.target_ci_id)
   AND NOT EXISTS (SELECT 1 FROM ci_relationship e WHERE e.id = r.id);
INSERT INTO collector_certificate
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::collector_certificate, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'collector_certificate'
   AND q.reason = 'SR-03 (WP-876): collector_certificate.collector_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM collector x WHERE x.id = r.collector_id)
   AND NOT EXISTS (SELECT 1 FROM collector_certificate e WHERE e.id = r.id);
INSERT INTO discovery_job
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::discovery_job, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'discovery_job'
   AND q.reason = 'SR-03 (WP-876): discovery_job.collector_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM collector x WHERE x.id = r.collector_id)
   AND NOT EXISTS (SELECT 1 FROM discovery_job e WHERE e.id = r.id);
INSERT INTO discovery_result
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::discovery_result, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'discovery_result'
   AND q.reason = 'SR-03 (WP-876): discovery_result.job_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM discovery_job x WHERE x.id = r.job_id)
   AND NOT EXISTS (SELECT 1 FROM discovery_result e WHERE e.id = r.id);
INSERT INTO discovery_scope
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::discovery_scope, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'discovery_scope'
   AND q.reason = 'SR-03 (WP-876): discovery_scope.collector_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM collector x WHERE x.id = r.collector_id)
   AND NOT EXISTS (SELECT 1 FROM discovery_scope e WHERE e.id = r.id);
INSERT INTO network_interface
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::network_interface, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'network_interface'
   AND q.reason = 'SR-03 (WP-876): network_interface.ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.ci_id)
   AND NOT EXISTS (SELECT 1 FROM network_interface e WHERE e.id = r.id);
INSERT INTO rack
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::rack, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'rack'
   AND q.reason = 'SR-03 (WP-876): rack.room_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM room x WHERE x.id = r.room_id)
   AND NOT EXISTS (SELECT 1 FROM rack e WHERE e.id = r.id);
INSERT INTO rack_mount
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::rack_mount, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'rack_mount'
   AND q.reason = 'SR-03 (WP-876): rack_mount.ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.ci_id)
   AND NOT EXISTS (SELECT 1 FROM rack_mount e WHERE e.id = r.id);
INSERT INTO rack_mount
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::rack_mount, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'rack_mount'
   AND q.reason = 'SR-03 (WP-876): rack_mount.rack_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM rack x WHERE x.id = r.rack_id)
   AND NOT EXISTS (SELECT 1 FROM rack_mount e WHERE e.id = r.id);
INSERT INTO relationship_suppression
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::relationship_suppression, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'relationship_suppression'
   AND q.reason = 'SR-03 (WP-876): relationship_suppression.source_ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.source_ci_id)
   AND NOT EXISTS (SELECT 1 FROM relationship_suppression e WHERE e.id = r.id);
INSERT INTO relationship_suppression
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::relationship_suppression, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'relationship_suppression'
   AND q.reason = 'SR-03 (WP-876): relationship_suppression.target_ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.target_ci_id)
   AND NOT EXISTS (SELECT 1 FROM relationship_suppression e WHERE e.id = r.id);
INSERT INTO role_assignment
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::role_assignment, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'role_assignment'
   AND q.reason = 'SR-03 (WP-876): role_assignment.role_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM role x WHERE x.id = r.role_id)
   AND NOT EXISTS (SELECT 1 FROM role_assignment e WHERE e.id = r.id);
INSERT INTO role_assignment
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::role_assignment, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'role_assignment'
   AND q.reason = 'SR-03 (WP-876): role_assignment.user_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.user_id)
   AND NOT EXISTS (SELECT 1 FROM role_assignment e WHERE e.id = r.id);
INSERT INTO role_permission
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::role_permission, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'role_permission'
   AND q.reason = 'SR-03 (WP-876): role_permission.role_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM role x WHERE x.id = r.role_id)
   AND NOT EXISTS (SELECT 1 FROM role_permission e WHERE e.organization_id = r.organization_id AND e.role_id = r.role_id AND e.permission_key = r.permission_key);
INSERT INTO room
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::room, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'room'
   AND q.reason = 'SR-03 (WP-876): room.building_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM building x WHERE x.id = r.building_id)
   AND NOT EXISTS (SELECT 1 FROM room e WHERE e.id = r.id);
INSERT INTO service_account_role
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::service_account_role, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'service_account_role'
   AND q.reason = 'SR-03 (WP-876): service_account_role.role_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM role x WHERE x.id = r.role_id)
   AND NOT EXISTS (SELECT 1 FROM service_account_role e WHERE e.id = r.id);
INSERT INTO team_member
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::team_member, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'team_member'
   AND q.reason = 'SR-03 (WP-876): team_member.team_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM team x WHERE x.id = r.team_id)
   AND NOT EXISTS (SELECT 1 FROM team_member e WHERE e.id = r.id);
INSERT INTO team_member
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::team_member, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'team_member'
   AND q.reason = 'SR-03 (WP-876): team_member.user_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.user_id)
   AND NOT EXISTS (SELECT 1 FROM team_member e WHERE e.id = r.id);
INSERT INTO user_custom_role
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::user_custom_role, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'user_custom_role'
   AND q.reason = 'SR-03 (WP-876): user_custom_role.custom_role_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM custom_role x WHERE x.id = r.custom_role_id)
   AND NOT EXISTS (SELECT 1 FROM user_custom_role e WHERE e.id = r.id);
INSERT INTO user_custom_role
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::user_custom_role, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'user_custom_role'
   AND q.reason = 'SR-03 (WP-876): user_custom_role.user_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.user_id)
   AND NOT EXISTS (SELECT 1 FROM user_custom_role e WHERE e.id = r.id);
INSERT INTO user_invitation
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::user_invitation, q.row_data) AS r
 WHERE q.migration = '000287' AND q.source_table = 'user_invitation'
   AND q.reason = 'SR-03 (WP-876): user_invitation.role_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM role x WHERE x.id = r.role_id)
   AND NOT EXISTS (SELECT 1 FROM user_invitation e WHERE e.id = r.id);
ALTER TABLE agent_enrollment_token ENABLE TRIGGER USER;
ALTER TABLE api_key ENABLE TRIGGER USER;
ALTER TABLE asset_location ENABLE TRIGGER USER;
ALTER TABLE building ENABLE TRIGGER USER;
ALTER TABLE cable ENABLE TRIGGER USER;
ALTER TABLE ci ENABLE TRIGGER USER;
ALTER TABLE ci_change ENABLE TRIGGER USER;
ALTER TABLE ci_contact ENABLE TRIGGER USER;
ALTER TABLE ci_field_value ENABLE TRIGGER USER;
ALTER TABLE ci_instance_field_definition ENABLE TRIGGER USER;
ALTER TABLE ci_merge ENABLE TRIGGER USER;
ALTER TABLE ci_relationship ENABLE TRIGGER USER;
ALTER TABLE collector ENABLE TRIGGER USER;
ALTER TABLE collector_certificate ENABLE TRIGGER USER;
ALTER TABLE collector_enrollment_code ENABLE TRIGGER USER;
ALTER TABLE contact ENABLE TRIGGER USER;
ALTER TABLE discovery_job ENABLE TRIGGER USER;
ALTER TABLE discovery_result ENABLE TRIGGER USER;
ALTER TABLE discovery_scope ENABLE TRIGGER USER;
ALTER TABLE endpoint_agent ENABLE TRIGGER USER;
ALTER TABLE ip_address ENABLE TRIGGER USER;
ALTER TABLE network_interface ENABLE TRIGGER USER;
ALTER TABLE pii_subject ENABLE TRIGGER USER;
ALTER TABLE rack ENABLE TRIGGER USER;
ALTER TABLE rack_mount ENABLE TRIGGER USER;
ALTER TABLE relationship_suppression ENABLE TRIGGER USER;
ALTER TABLE role_assignment ENABLE TRIGGER USER;
ALTER TABLE role_permission ENABLE TRIGGER USER;
ALTER TABLE room ENABLE TRIGGER USER;
ALTER TABLE service_account_role ENABLE TRIGGER USER;
ALTER TABLE subnet ENABLE TRIGGER USER;
ALTER TABLE team ENABLE TRIGGER USER;
ALTER TABLE team_member ENABLE TRIGGER USER;
ALTER TABLE user_custom_role ENABLE TRIGGER USER;
ALTER TABLE user_invitation ENABLE TRIGGER USER;
ALTER TABLE vlan ENABLE TRIGGER USER;

DELETE FROM migration_quarantine WHERE migration = '000287';
