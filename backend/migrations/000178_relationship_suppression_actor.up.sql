-- Migration 000178: relationship_suppression records who suppressed an edge
-- and why (WP-584, REL-03, REL-04; F-273).
--
-- "Entfernen und unterdrücken" stored the suppression with its CIs, type and
-- created_at only: who removed the edge and for what reason was not kept,
-- and a suppression could be lifted only in the database. created_by is the
-- user who deleted the discovery edge with force (NULL for a principal
-- without user, or after the user was deleted), reason the reason given in
-- the confirmation. Like the references of 000177 the user belongs to the
-- organization of the suppression (composite foreign key; app_user has the
-- unique key (id, organization_id) since 000177). The policies (ENABLE +
-- FORCE, suppression_isolation with USING and WITH CHECK, the scoped UPDATE
-- and DELETE policies of 000063/000142) are unchanged.

ALTER TABLE relationship_suppression
    ADD COLUMN created_by uuid,
    ADD COLUMN reason text,
    ADD CONSTRAINT relationship_suppression_created_by_fkey FOREIGN KEY (created_by, organization_id)
        REFERENCES app_user (id, organization_id) ON DELETE SET NULL (created_by),
    ADD CONSTRAINT relationship_suppression_reason_check
        CHECK (reason IS NULL OR char_length(reason) BETWEEN 1 AND 1000);
