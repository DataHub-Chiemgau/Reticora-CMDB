-- Revert migration 000025: restore the original ci_relationship rel_type CHECK.
-- Discovery-derived `powered_by` edges are dropped first, otherwise the
-- reinstated CHECK constraint could not be validated.

DELETE FROM ci_relationship WHERE rel_type = 'powered_by';

DROP INDEX IF EXISTS idx_ci_rel_source_kind;

ALTER TABLE ci_relationship DROP CONSTRAINT IF EXISTS ci_relationship_rel_type_check;
ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_rel_type_check
    CHECK (rel_type IN (
        'connected_to', 'hosted_on', 'depends_on', 'member_of',
        'powers', 'stores', 'monitors', 'backs_up'
    ));
