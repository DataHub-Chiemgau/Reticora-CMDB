-- Reverts 000308: the propagation and default-group triggers, the column
-- probe.collector_group_id, the tables probe_assignment,
-- collector_group_member and collector_group with their functions; the key
-- collector_id_organization_key goes only when nothing references it any
-- more.

DROP TRIGGER trg_probe_assignment_scope ON probe;
DROP FUNCTION propagate_probe_assignment_scope();
DROP TRIGGER trg_collector_group_member_moved ON collector_group_member;
DROP TRIGGER trg_collector_group_member_left ON collector_group_member;
DROP FUNCTION drop_member_probe_assignments();
DROP TRIGGER trg_collector_group_scope ON collector_group;
DROP FUNCTION propagate_collector_group_scope();
DROP TRIGGER collector_group_follow_collector ON collector;
DROP FUNCTION collector_group_follow_collector();
DROP TRIGGER collector_default_group ON collector;
DROP FUNCTION collector_default_group();
DROP FUNCTION collector_group_name(text, uuid);

DROP TRIGGER probe_collector_group_client ON probe;
DROP FUNCTION probe_collector_group_client();
DROP INDEX idx_probe_org_collector_group;
ALTER TABLE probe DROP CONSTRAINT probe_collector_group_fkey;
ALTER TABLE probe DROP COLUMN collector_group_id;

DROP TABLE probe_assignment;
DROP FUNCTION probe_assignment_scope();
DROP TABLE collector_group_member;
DROP FUNCTION collector_group_member_scope();
DROP TABLE collector_group;
DROP FUNCTION collector_group_site_client();

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
