-- Migration 000283: CI types hidden per client (MET-05, WP-858).
--
-- A CI type is deactivated for the whole organization (WP-249). A client
-- (customer) can hide a type instead: the type stays active, but the type
-- lists for the client no longer offer it and a CI of the client cannot be
-- created with it or changed to it by a person. Existing CIs of the type
-- stay readable and editable; discovery and ingest are not affected. The
-- choice belongs to the client, not to the user.
--
-- 1. client_ci_type_hidden (organization_id, client_id, ci_type_id,
--    created_at, created_by), primary key over all three ids. The client is
--    referenced with the organization (TEN-05, 000136), deleting it removes
--    its entries. ci_type_id references ci_type; the single-column foreign
--    key bypasses row level security, so the trigger
--    client_ci_type_hidden_type refuses a private type of another
--    organization (as for attribute_definition: a system type or a type of
--    the same organization).
-- 2. Row level security with ENABLE and FORCE: the organization and the
--    client scope (client_id is NOT NULL, so USING equals WITH CHECK),
--    truncate guard (000144).

-- ─── 1. Table ─────────────────────────────────────────────────────────────────

CREATE TABLE client_ci_type_hidden (
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID NOT NULL,
    ci_type_id      UUID NOT NULL REFERENCES ci_type (id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID,
    PRIMARY KEY (organization_id, client_id, ci_type_id),
    CONSTRAINT client_ci_type_hidden_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_client_ci_type_hidden_type ON client_ci_type_hidden (ci_type_id);

CREATE FUNCTION client_ci_type_hidden_type() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM ci_type t WHERE t.id = NEW.ci_type_id
                     AND (t.organization_id IS NULL OR t.organization_id = NEW.organization_id)) THEN
        RAISE EXCEPTION 'CI type % is not a type of the organization', NEW.ci_type_id
            USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'client_ci_type_hidden_ci_type_id_fkey';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER client_ci_type_hidden_type BEFORE INSERT OR UPDATE OF ci_type_id, organization_id
    ON client_ci_type_hidden FOR EACH ROW EXECUTE FUNCTION client_ci_type_hidden_type();

-- ─── 2. Row level security ────────────────────────────────────────────────────

ALTER TABLE client_ci_type_hidden ENABLE ROW LEVEL SECURITY;
ALTER TABLE client_ci_type_hidden FORCE ROW LEVEL SECURITY;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON client_ci_type_hidden
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE POLICY client_ci_type_hidden_isolation ON client_ci_type_hidden
    USING (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
