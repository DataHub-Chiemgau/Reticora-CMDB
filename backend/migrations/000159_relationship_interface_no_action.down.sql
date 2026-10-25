-- Reverts 000159: the interface ends of ci_relationship cascade from
-- network_interface again (000114).

ALTER TABLE ci_relationship
    DROP CONSTRAINT ci_relationship_target_interface_id_fkey,
    ADD CONSTRAINT ci_relationship_target_interface_id_fkey
        FOREIGN KEY (target_interface_id) REFERENCES network_interface (id) ON DELETE CASCADE,
    DROP CONSTRAINT ci_relationship_source_interface_id_fkey,
    ADD CONSTRAINT ci_relationship_source_interface_id_fkey
        FOREIGN KEY (source_interface_id) REFERENCES network_interface (id) ON DELETE CASCADE;
