-- The merge of responsible into owner cannot be told apart again; the links
-- stay owner.
ALTER TABLE ci_contact DROP CONSTRAINT chk_contact_rel_type;
UPDATE ci_contact SET relationship_type = 'operator' WHERE relationship_type = 'operations';
ALTER TABLE ci_contact ALTER COLUMN relationship_type SET DEFAULT 'responsible';
ALTER TABLE ci_contact ADD CONSTRAINT chk_contact_rel_type
    CHECK (relationship_type IN ('responsible', 'owner', 'operator', 'vendor', 'escalation'));
