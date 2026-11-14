-- Reverts 000300: triggers, functions, index and the columns parent_id and
-- is_pool go. subnet_id_organization_key stays: migration 000287 created it
-- and other foreign keys (ip_address, credential_binding) reference it.
DROP TRIGGER trg_subnet_unplace ON subnet;
DROP TRIGGER trg_subnet_place_update ON subnet;
DROP TRIGGER trg_subnet_place_insert ON subnet;
DROP FUNCTION subnet_unplace();
DROP FUNCTION subnet_place();
DROP FUNCTION subnet_overlaps(uuid);
DROP FUNCTION subnet_overlap_partners(uuid);
DROP FUNCTION subnet_tree_lock(uuid, uuid);
DROP FUNCTION subnet_narrowest(uuid, uuid, cidr, uuid);
DROP INDEX idx_subnet_org_vrf_parent;
ALTER TABLE subnet DROP CONSTRAINT subnet_parent_fkey;
ALTER TABLE subnet
    DROP COLUMN is_pool,
    DROP COLUMN parent_id;
