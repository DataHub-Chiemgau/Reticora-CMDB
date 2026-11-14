-- Reverts WP-880: entitlement.origin and organization.plan_changed_at go.
ALTER TABLE organization DROP COLUMN plan_changed_at;
ALTER TABLE entitlement DROP CONSTRAINT entitlement_origin_check;
ALTER TABLE entitlement DROP COLUMN origin;
