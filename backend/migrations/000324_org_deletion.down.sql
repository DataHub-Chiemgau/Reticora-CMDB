-- Reverts 000324: the transition trigger, the index and the deletion
-- columns of organization.

DROP TRIGGER organization_deletion_transition ON organization;
DROP FUNCTION organization_deletion_transition();
DROP INDEX idx_organization_deletion_due;
ALTER TABLE organization
    DROP CONSTRAINT organization_deletion_dates_check,
    DROP CONSTRAINT organization_deletion_state_check,
    DROP COLUMN deletion_requested_by,
    DROP COLUMN deletion_due_at,
    DROP COLUMN deletion_requested_at,
    DROP COLUMN deletion_state;
