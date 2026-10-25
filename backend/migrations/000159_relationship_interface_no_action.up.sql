-- WP-424 (REL-05, REL-02; F-042): an edge no longer disappears silently with
-- an interface it names as an end.
--
-- source_interface_id and target_interface_id (000114) cascaded from
-- network_interface: when discovery removed an interface after three misses
-- (REC-07), its manual and verified edges went with it in the discovery
-- transaction, without ci_change or audit. Both keys are now NO ACTION, not
-- deferrable: a delete of an interface that an edge still names fails at the
-- end of the statement. The repositories decide before they delete an
-- interface (ipam/pg_repository.go): a manual or verified edge keeps it, an
-- unverified discovery edge is removed and recorded in ci_change.
--
-- The hard delete of a CI keeps working: its interfaces (000015) and its
-- edges (000006, 000056) cascade from the CI in the same statement, so no
-- edge names a deleted interface when the key is checked. The same holds
-- for the delete of an organization.

ALTER TABLE ci_relationship
    DROP CONSTRAINT ci_relationship_source_interface_id_fkey,
    ADD CONSTRAINT ci_relationship_source_interface_id_fkey
        FOREIGN KEY (source_interface_id) REFERENCES network_interface (id) ON DELETE NO ACTION,
    DROP CONSTRAINT ci_relationship_target_interface_id_fkey,
    ADD CONSTRAINT ci_relationship_target_interface_id_fkey
        FOREIGN KEY (target_interface_id) REFERENCES network_interface (id) ON DELETE NO ACTION;
