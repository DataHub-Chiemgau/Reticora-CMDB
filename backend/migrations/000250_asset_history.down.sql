-- Reverts WP-794: entity_change without client and site scope and only the
-- organization policy of 000055; the asset trail is no longer written by the
-- database.

DROP POLICY entity_change_scoped_delete ON entity_change;
DROP POLICY entity_change_scoped_update ON entity_change;
DROP POLICY entity_change_isolation ON entity_change;
CREATE POLICY entity_change_isolation ON entity_change
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER ci_history_scope_propagation ON ci;
DROP TRIGGER asset_history_scope_propagation ON asset;
DROP FUNCTION propagate_entity_change_scope();
DROP TRIGGER entity_change_derive_scope ON entity_change;
DROP FUNCTION entity_change_derive_scope();
DROP TRIGGER asset_history ON asset;
DROP FUNCTION asset_history();
DROP FUNCTION asset_history_form(asset);

DROP INDEX IF EXISTS idx_entity_change_org_entity;
ALTER TABLE entity_change DROP COLUMN site_id;
ALTER TABLE entity_change DROP COLUMN client_id;
