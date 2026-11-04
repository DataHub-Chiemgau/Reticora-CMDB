-- Down of 000223: the supplier table with its indexes, triggers and
-- policies, then the unique key of contact this migration added.

DROP TABLE supplier;
ALTER TABLE contact DROP CONSTRAINT contact_id_organization_key;
