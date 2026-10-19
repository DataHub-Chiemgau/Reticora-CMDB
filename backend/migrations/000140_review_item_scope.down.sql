-- Reverts WP-342: the organization-only policy of migration 000021, no
-- client or site on review_item.

DROP POLICY review_item_scoped_delete ON review_item;
DROP POLICY review_item_scoped_update ON review_item;
DROP POLICY review_item_scoped_select ON review_item;
DROP POLICY review_item_isolation ON review_item;
CREATE POLICY review_item_isolation ON review_item
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER ci_review_item_scope_propagation ON ci;
DROP FUNCTION propagate_review_item_scope();
DROP TRIGGER review_item_scope ON review_item;
DROP FUNCTION derive_review_item_scope();

DROP INDEX idx_review_item_candidates;
DROP INDEX idx_review_item_client;
ALTER TABLE review_item DROP COLUMN site_id, DROP COLUMN client_id;
ALTER TABLE site DROP CONSTRAINT site_id_organization_id_key;
