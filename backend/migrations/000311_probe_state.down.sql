-- Reverts 000311: the propagation trigger on probe, the tables
-- probe_ingest_cursor, probe_state_change and probe_state with their scope
-- function; the key collector_id_organization_key goes only when nothing
-- references it any more.

DROP TRIGGER trg_probe_state_scope ON probe;
DROP FUNCTION propagate_probe_state_scope();
DROP TABLE probe_ingest_cursor;
DROP TABLE probe_state_change;
DROP TABLE probe_state;
DROP FUNCTION probe_state_scope();

-- collector-key:begin
DO $$
DECLARE
    key_index oid;
BEGIN
    SELECT conindid INTO key_index FROM pg_constraint
     WHERE conrelid = 'collector'::regclass AND conname = 'collector_id_organization_key';
    IF key_index IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f WHERE f.contype = 'f' AND f.confrelid = 'collector'::regclass AND f.conindid = key_index
    ) THEN
        ALTER TABLE collector DROP CONSTRAINT collector_id_organization_key;
    END IF;
END
$$;
-- collector-key:end
