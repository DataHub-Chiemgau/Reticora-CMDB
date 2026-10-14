-- TEN-05 (WP-208): a client_id referenced client(id) alone. Foreign keys are
-- checked without row level security, so a write of organization B could
-- reference a client of organization A (found by the channel matrix: sites,
-- CIs, contacts and subnets accepted a foreign client). The references now
-- include the organization, so a client is only valid in its own one.
ALTER TABLE client ADD CONSTRAINT client_id_organization_key UNIQUE (id, organization_id);

ALTER TABLE agent_enrollment_token DROP CONSTRAINT agent_enrollment_token_client_id_fkey,
    ADD CONSTRAINT agent_enrollment_token_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE CASCADE;
ALTER TABLE asset DROP CONSTRAINT asset_client_id_fkey,
    ADD CONSTRAINT asset_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE ci DROP CONSTRAINT ci_client_id_fkey,
    ADD CONSTRAINT ci_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE ci_merge DROP CONSTRAINT ci_merge_client_id_fkey,
    ADD CONSTRAINT ci_merge_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE CASCADE;
ALTER TABLE collector DROP CONSTRAINT collector_client_id_fkey,
    ADD CONSTRAINT collector_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE consumable DROP CONSTRAINT consumable_client_id_fkey,
    ADD CONSTRAINT consumable_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE contact DROP CONSTRAINT contact_client_id_fkey,
    ADD CONSTRAINT contact_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE SET NULL (client_id);
ALTER TABLE credential DROP CONSTRAINT credential_client_id_fkey,
    ADD CONSTRAINT credential_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE discovery_scope DROP CONSTRAINT discovery_scope_client_id_fkey,
    ADD CONSTRAINT discovery_scope_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE CASCADE;
ALTER TABLE form_def DROP CONSTRAINT form_def_client_id_fkey,
    ADD CONSTRAINT form_def_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE SET NULL (client_id);
ALTER TABLE internal_order DROP CONSTRAINT internal_order_client_id_fkey,
    ADD CONSTRAINT internal_order_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE key_item DROP CONSTRAINT key_item_client_id_fkey,
    ADD CONSTRAINT key_item_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE location DROP CONSTRAINT location_client_id_fkey,
    ADD CONSTRAINT location_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE CASCADE;
ALTER TABLE maintenance_notification DROP CONSTRAINT maintenance_notification_client_id_fkey,
    ADD CONSTRAINT maintenance_notification_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE quantity_item DROP CONSTRAINT quantity_item_client_id_fkey,
    ADD CONSTRAINT quantity_item_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE SET NULL (client_id);
ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_scope_client_id_fkey,
    ADD CONSTRAINT role_assignment_scope_client_id_fkey FOREIGN KEY (scope_client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE service_account_role DROP CONSTRAINT service_account_role_scope_client_id_fkey,
    ADD CONSTRAINT service_account_role_scope_client_id_fkey FOREIGN KEY (scope_client_id, organization_id) REFERENCES client (id, organization_id);
ALTER TABLE site DROP CONSTRAINT site_client_id_fkey,
    ADD CONSTRAINT site_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE CASCADE;
ALTER TABLE sla DROP CONSTRAINT sla_client_id_fkey,
    ADD CONSTRAINT sla_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE SET NULL (client_id);
ALTER TABLE subnet DROP CONSTRAINT subnet_client_id_fkey,
    ADD CONSTRAINT subnet_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE SET NULL (client_id);
ALTER TABLE vlan DROP CONSTRAINT vlan_client_id_fkey,
    ADD CONSTRAINT vlan_client_id_fkey FOREIGN KEY (client_id, organization_id) REFERENCES client (id, organization_id) ON DELETE CASCADE;
