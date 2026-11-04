-- WP-293: removes the foreign reference of tickets and ticket comments.

DROP INDEX IF EXISTS uq_ticket_comment_org_external_ref;
DROP INDEX IF EXISTS uq_ticket_org_external_ref;

ALTER TABLE ticket_comment
    DROP CONSTRAINT IF EXISTS ticket_comment_external_ref_pair_check,
    DROP COLUMN IF EXISTS external_ref,
    DROP COLUMN IF EXISTS external_provider;

ALTER TABLE ticket
    DROP CONSTRAINT IF EXISTS ticket_external_ref_pair_check,
    DROP COLUMN IF EXISTS external_ref,
    DROP COLUMN IF EXISTS external_provider;
