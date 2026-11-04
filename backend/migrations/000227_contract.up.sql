-- Migration 000227: contracts per organization and client (MGT-12, API-06,
-- WP-269, E-41, E-119).
--
-- 1. contract holds the contract of an organization or a client: number
--    (unique per organization as a column constraint, K22), kind (E-119,
--    English keys after GLO-10), term (start_date, end_date), notice period
--    in days, value and currency (ISO 4217; the service fills a missing one
--    from organization.currency, a column default cannot name the
--    organization), status (draft, active, terminated, ended; WP-270
--    filters on it) and notes. version counts the changes for ETag and
--    If-Match (API-07). client_id NULL is an org-wide contract. UNIQUE
--    (id, organization_id) is the target of asset.contract_id (WP-271).
-- 2. The client and supplier references carry the organization (K19,
--    pattern 000136) and have no cascade: a client or supplier with
--    contracts cannot be deleted (422).
-- 3. Row level security with ENABLE and FORCE: USING shows org-wide
--    contracts to a client-scoped principal, WITH CHECK only lets it write
--    contracts of its clients (E-09); the restrictive UPDATE and DELETE
--    policies keep it from changing org-wide rows (pattern 000129 with the
--    client predicate only). Truncate guard of WP-352.
-- 4. The AFTER DELETE trigger delete_object_grants('contract') (000226,
--    WP-264) removes the object grants of a deleted contract.

CREATE TABLE contract (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id          UUID,
    supplier_id        UUID,
    number             TEXT NOT NULL CONSTRAINT contract_number_check CHECK (btrim(number) <> ''),
    kind               TEXT,
    start_date         DATE,
    end_date           DATE,
    notice_period_days INTEGER,
    value              NUMERIC,
    currency           TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'active',
    notes              TEXT,
    version            BIGINT NOT NULL DEFAULT 1,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT contract_organization_id_number_key UNIQUE (organization_id, number),
    CONSTRAINT contract_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT contract_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    CONSTRAINT contract_supplier_id_fkey FOREIGN KEY (supplier_id, organization_id)
        REFERENCES supplier (id, organization_id),
    CONSTRAINT contract_dates_check CHECK (end_date >= start_date),
    CONSTRAINT contract_notice_period_days_check CHECK (notice_period_days >= 0),
    CONSTRAINT contract_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT contract_kind_check
        CHECK (kind IN ('maintenance', 'support', 'lease', 'license', 'purchase', 'service', 'other')),
    CONSTRAINT contract_status_check CHECK (status IN ('draft', 'active', 'terminated', 'ended'))
);
CREATE INDEX idx_contract_end_date ON contract (organization_id, end_date);
CREATE INDEX idx_contract_supplier ON contract (supplier_id, organization_id);
CREATE INDEX idx_contract_client ON contract (organization_id, client_id);
CREATE INDEX idx_contract_created ON contract (organization_id, created_at, id);
CREATE TRIGGER trg_contract_updated_at BEFORE UPDATE ON contract
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER contract_object_grants AFTER DELETE ON contract
    FOR EACH ROW EXECUTE FUNCTION delete_object_grants('contract');

ALTER TABLE contract ENABLE ROW LEVEL SECURITY;
ALTER TABLE contract FORCE ROW LEVEL SECURITY;
CREATE POLICY contract_isolation ON contract FOR ALL
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY contract_scoped_update ON contract AS RESTRICTIVE FOR UPDATE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY contract_scoped_delete ON contract AS RESTRICTIVE FOR DELETE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON contract
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
