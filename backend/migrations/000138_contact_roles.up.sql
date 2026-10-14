-- AUD-02: the roles of ci_contact are operations, escalation, owner and
-- vendor. operator becomes operations; responsible (not in the catalog)
-- becomes owner, a link that already exists as owner is dropped.
ALTER TABLE ci_contact DROP CONSTRAINT chk_contact_rel_type;
DELETE FROM ci_contact r
WHERE r.relationship_type = 'responsible'
  AND EXISTS (SELECT 1 FROM ci_contact o
              WHERE o.ci_id = r.ci_id AND o.contact_id = r.contact_id AND o.relationship_type = 'owner');
UPDATE ci_contact SET relationship_type = CASE relationship_type
    WHEN 'operator' THEN 'operations'
    WHEN 'responsible' THEN 'owner'
    ELSE relationship_type END
WHERE relationship_type IN ('operator', 'responsible');
ALTER TABLE ci_contact ALTER COLUMN relationship_type SET DEFAULT 'owner';
ALTER TABLE ci_contact ADD CONSTRAINT chk_contact_rel_type
    CHECK (relationship_type IN ('operations', 'escalation', 'owner', 'vendor'));
