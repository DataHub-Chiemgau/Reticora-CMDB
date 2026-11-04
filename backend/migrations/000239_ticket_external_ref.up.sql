-- WP-293 (TKT-03 [O], CH17, E-47): the foreign reference of a ticket and of
-- a ticket comment for the ticket provider interface (ticket.Provider).
--
-- * external_provider names the provider (e.g. internal, later an
--   integration), external_ref the id of the object in that provider; named
--   like client.external_ref (000031). Both are set together or not at all
--   (ticket_external_ref_pair_check, ticket_comment_external_ref_pair_check,
--   23514).
-- * A reference is unique per organization and provider
--   (uq_ticket_org_external_ref, uq_ticket_comment_org_external_ref; partial
--   unique indexes without expressions, K22; a duplicate is 23505).
-- * RLS and policies stay unchanged.

ALTER TABLE ticket
    ADD COLUMN external_provider text,
    ADD COLUMN external_ref text,
    ADD CONSTRAINT ticket_external_ref_pair_check
        CHECK ((external_provider IS NULL) = (external_ref IS NULL));

ALTER TABLE ticket_comment
    ADD COLUMN external_provider text,
    ADD COLUMN external_ref text,
    ADD CONSTRAINT ticket_comment_external_ref_pair_check
        CHECK ((external_provider IS NULL) = (external_ref IS NULL));

CREATE UNIQUE INDEX IF NOT EXISTS uq_ticket_org_external_ref
    ON ticket (organization_id, external_provider, external_ref)
    WHERE external_ref IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_ticket_comment_org_external_ref
    ON ticket_comment (organization_id, external_provider, external_ref)
    WHERE external_ref IS NOT NULL;
