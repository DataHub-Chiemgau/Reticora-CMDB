-- Reverts 000362: the locked fields of the lifecycle states.

ALTER TABLE lifecycle_state DROP COLUMN locked_fields;
