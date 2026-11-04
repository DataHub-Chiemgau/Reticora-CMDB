-- Reverts 000253: composition and asset.parent_asset_id come back in the
-- schema state before the migration, then their rows from
-- migration_quarantine, whose entries of 000253 go.
--
-- Schema: the table of 000055 with composition_child_check, the indexes,
-- ENABLE and FORCE ROW LEVEL SECURITY, the policy composition_isolation and
-- trg_composition_updated_at; the tenant foreign keys of 000056; the cycle
-- function and trg_composition_no_cycle of 000057 (same text); the truncate
-- guard of 000144; the column asset.parent_asset_id with
-- asset_parent_asset_id_fkey (000055). The privileges of reticora_app come
-- from the default privileges (000056).
--
-- Rows: a saved row is restored when the rows it references still exist;
-- one whose asset, CI or organization was deleted after the up migration
-- would have gone with it (ON DELETE CASCADE, for asset.parent_asset_id ON
-- DELETE SET NULL) and stays away. The asset values are written with the
-- row triggers for updated_at, version and history disabled, so the asset
-- rows come back as they were.

CREATE TABLE composition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    parent_asset_id UUID NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
    child_ci_id UUID REFERENCES ci(id) ON DELETE CASCADE,
    child_asset_id UUID REFERENCES asset(id) ON DELETE CASCADE,
    role TEXT,
    position TEXT,
    configuration_only BOOLEAN NOT NULL DEFAULT true,
    independently_serialized BOOLEAN NOT NULL DEFAULT false,
    independently_assignable BOOLEAN NOT NULL DEFAULT false,
    independently_locatable BOOLEAN NOT NULL DEFAULT false,
    independently_lifecycle_managed BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT composition_child_check CHECK (
        (child_ci_id IS NOT NULL AND child_asset_id IS NULL) OR
        (child_ci_id IS NULL AND child_asset_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX idx_composition_unique_child_ci ON composition(parent_asset_id, child_ci_id) WHERE child_ci_id IS NOT NULL;
CREATE UNIQUE INDEX idx_composition_unique_child_asset ON composition(parent_asset_id, child_asset_id) WHERE child_asset_id IS NOT NULL;
CREATE INDEX idx_composition_parent ON composition(parent_asset_id);

ALTER TABLE composition ENABLE ROW LEVEL SECURITY;
ALTER TABLE composition FORCE ROW LEVEL SECURITY;
CREATE POLICY composition_isolation ON composition
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE TRIGGER trg_composition_updated_at BEFORE UPDATE ON composition
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE composition
    ADD CONSTRAINT composition_parent_tenant_fkey
    FOREIGN KEY (parent_asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE CASCADE;
ALTER TABLE composition
    ADD CONSTRAINT composition_child_ci_tenant_fkey
    FOREIGN KEY (child_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE composition
    ADD CONSTRAINT composition_child_asset_tenant_fkey
    FOREIGN KEY (child_asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE CASCADE;

CREATE OR REPLACE FUNCTION reject_composition_cycle() RETURNS TRIGGER AS $$
DECLARE
    ancestor UUID;
    depth INT := 0;
BEGIN
    IF NEW.child_asset_id IS NULL THEN
        RETURN NEW;
    END IF;

    IF NEW.child_asset_id = NEW.parent_asset_id THEN
        RAISE EXCEPTION 'composition cycle: an asset cannot be its own parent'
            USING ERRCODE = '23514';
    END IF;

    ancestor := NEW.parent_asset_id;
    WHILE ancestor IS NOT NULL LOOP
        depth := depth + 1;
        -- Defensive bound: a pre-existing cycle must not spin forever.
        IF depth > 100 THEN
            RAISE EXCEPTION 'composition ancestry deeper than supported limit'
                USING ERRCODE = '23514';
        END IF;

        SELECT c.parent_asset_id INTO ancestor
        FROM composition c
        WHERE c.child_asset_id = ancestor
          AND c.organization_id = NEW.organization_id
          AND (TG_OP = 'INSERT' OR c.id <> NEW.id);

        IF ancestor = NEW.child_asset_id THEN
            RAISE EXCEPTION 'composition cycle: asset % is already an ancestor of the requested parent',
                NEW.child_asset_id USING ERRCODE = '23514';
        END IF;
    END LOOP;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_composition_no_cycle
    BEFORE INSERT OR UPDATE OF parent_asset_id, child_asset_id ON composition
    FOR EACH ROW EXECUTE FUNCTION reject_composition_cycle();

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON composition
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE asset ADD COLUMN parent_asset_id UUID REFERENCES asset(id) ON DELETE SET NULL;

-- ─── Rows ────────────────────────────────────────────────────────────────────

ALTER TABLE asset DISABLE TRIGGER trg_asset_updated_at;
ALTER TABLE asset DISABLE TRIGGER asset_version_bump;
ALTER TABLE asset DISABLE TRIGGER asset_history;

UPDATE asset a
SET parent_asset_id = (q.row_data->>'parent_asset_id')::uuid
FROM migration_quarantine q
WHERE q.migration = '000253' AND q.source_table = 'asset'
  AND a.id = (q.row_data->>'id')::uuid AND a.organization_id = q.organization_id
  AND EXISTS (SELECT 1 FROM asset p WHERE p.id = (q.row_data->>'parent_asset_id')::uuid);

ALTER TABLE asset ENABLE TRIGGER trg_asset_updated_at;
ALTER TABLE asset ENABLE TRIGGER asset_version_bump;
ALTER TABLE asset ENABLE TRIGGER asset_history;

INSERT INTO composition
SELECT r.*
FROM migration_quarantine q
CROSS JOIN LATERAL jsonb_populate_record(NULL::composition, q.row_data) r
WHERE q.migration = '000253' AND q.source_table = 'composition'
  AND EXISTS (SELECT 1 FROM organization o WHERE o.id = r.organization_id)
  AND EXISTS (SELECT 1 FROM asset a WHERE a.id = r.parent_asset_id AND a.organization_id = r.organization_id)
  AND (r.child_ci_id IS NULL
       OR EXISTS (SELECT 1 FROM ci c WHERE c.id = r.child_ci_id AND c.organization_id = r.organization_id))
  AND (r.child_asset_id IS NULL
       OR EXISTS (SELECT 1 FROM asset a WHERE a.id = r.child_asset_id AND a.organization_id = r.organization_id))
ORDER BY q.id;

DELETE FROM migration_quarantine WHERE migration = '000253';
