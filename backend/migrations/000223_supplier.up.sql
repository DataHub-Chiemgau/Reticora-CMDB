-- Migration 000223: suppliers per organization and client (MGT-12, API-06,
-- WP-736, E-41).
--
-- 1. supplier holds the master data of a supplier: name (the company name,
--    no PII, unique per organization as a column constraint, no expression
--    index, K22), external_ref, notes and the contact person only as a
--    reference to contact (PII in the vault, WP-242). version counts the
--    changes for ETag and If-Match (API-07). client_id NULL is an org-wide
--    supplier. UNIQUE (id, organization_id) is the target of contract
--    (WP-269), asset (WP-271) and internal_order (WP-286).
-- 2. The client and contact references carry the organization (K19,
--    pattern 000136); deleting the contact sets only contact_id to NULL.
--    contact gets UNIQUE (id, organization_id) as their target.
-- 3. Row level security with ENABLE and FORCE: USING shows org-wide
--    suppliers to a client-scoped principal, WITH CHECK only lets it write
--    suppliers of its clients (E-09); the restrictive UPDATE and DELETE
--    policies keep it from changing org-wide rows (pattern 000129 with the
--    client predicate only). Truncate guard of WP-352.

ALTER TABLE contact ADD CONSTRAINT contact_id_organization_key UNIQUE (id, organization_id);

CREATE TABLE supplier (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID,
    name            TEXT NOT NULL CONSTRAINT supplier_name_check CHECK (btrim(name) <> ''),
    external_ref    TEXT,
    contact_id      UUID,
    notes           TEXT,
    version         BIGINT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT supplier_organization_id_name_key UNIQUE (organization_id, name),
    CONSTRAINT supplier_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT supplier_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT supplier_contact_id_fkey FOREIGN KEY (contact_id, organization_id)
        REFERENCES contact (id, organization_id) ON DELETE SET NULL (contact_id)
);
CREATE INDEX idx_supplier_client ON supplier (organization_id, client_id);
CREATE INDEX idx_supplier_contact ON supplier (organization_id, contact_id);
CREATE INDEX idx_supplier_created ON supplier (organization_id, created_at, id);
CREATE TRIGGER trg_supplier_updated_at BEFORE UPDATE ON supplier
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE supplier ENABLE ROW LEVEL SECURITY;
ALTER TABLE supplier FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_isolation ON supplier FOR ALL
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY supplier_scoped_update ON supplier AS RESTRICTIVE FOR UPDATE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY supplier_scoped_delete ON supplier AS RESTRICTIVE FOR DELETE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON supplier
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
