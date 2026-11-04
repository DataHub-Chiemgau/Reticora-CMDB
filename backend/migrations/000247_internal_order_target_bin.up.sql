-- Migration 000247: target bin of an internal order (STK-05, WP-847).
--
-- internal_order.target_location_id names the bin the goods receipt books
-- into by default (WP-760). It references location with the organization
-- (K19; key location_id_organization_key of 000194) and is RESTRICT like
-- the other references to location (E-71, pattern 000157): a bin an order
-- names is not deleted, the delete reports the kind internal_order (LOC-11,
-- locations/dependencies.go). The kind bin and the client of the order are
-- checked by the order repository on every write; the visibility of the
-- order follows its client_id (rls.DerivedScopeTables).

ALTER TABLE internal_order ADD COLUMN target_location_id uuid,
    ADD CONSTRAINT internal_order_target_location_fkey FOREIGN KEY (target_location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_internal_order_target_location ON internal_order (target_location_id, organization_id)
    WHERE target_location_id IS NOT NULL;
