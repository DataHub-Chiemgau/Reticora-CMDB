-- Down of 000243: the change set of the bulk operations and the constraint
-- of its job reference go.

DROP TABLE bulk_change_set;

ALTER TABLE job DROP CONSTRAINT job_id_organization_key;
