-- Reverts 000266_assignment_drop: assignment is rebuilt exactly (000010
-- with its indexes, the policy assignment_tenant_isolation as of 000023,
-- trg_assignment_updated_at of 000036, the signature columns of 000047,
-- FORCE RLS of 000056 and the TRUNCATE guard of 000144) and gets its rows
-- from migration_quarantine and its signatures from
-- assignment_signature_legacy (a payload cleared since stays NULL).
-- doc_link_entity_type_check, derive_document_link_scope() (000242) and
-- propagate_asset_module_scope() (000249) return to their versions before
-- 000266; the moved links hang at assignment again, the quarantined links
-- come back. Then the quarantine entries of 000266 and the table
-- assignment_signature_legacy go.

CREATE TABLE IF NOT EXISTS assignment (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    asset_id        UUID REFERENCES asset(id),
    ci_id           UUID REFERENCES ci(id),
    assigned_to     UUID NOT NULL REFERENCES app_user(id),
    assigned_by     UUID NOT NULL REFERENCES app_user(id),
    assignment_type TEXT NOT NULL DEFAULT 'assignment',
    status          TEXT NOT NULL DEFAULT 'active',
    assigned_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    due_date        DATE,
    returned_at     TIMESTAMPTZ,
    return_condition TEXT,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    checkout_signature JSONB,
    return_signature   JSONB,
    CONSTRAINT assignment_type_check CHECK (assignment_type IN (
        'assignment', 'return', 'transfer'
    )),
    CONSTRAINT assignment_status_check CHECK (status IN (
        'active', 'returned', 'transferred', 'overdue', 'cancelled'
    )),
    CONSTRAINT assignment_target_check CHECK (asset_id IS NOT NULL OR ci_id IS NOT NULL)
);

CREATE INDEX idx_assignment_org ON assignment(organization_id);
CREATE INDEX idx_assignment_user ON assignment(assigned_to);
CREATE INDEX idx_assignment_asset ON assignment(asset_id);
CREATE INDEX idx_assignment_status ON assignment(organization_id, status);

COMMENT ON COLUMN assignment.checkout_signature IS 'digital handover signature: {image_base64|object_key, signer_name, signed_at, method}';
COMMENT ON COLUMN assignment.return_signature IS 'digital return signature: {image_base64|object_key, signer_name, signed_at, method}';

ALTER TABLE assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE assignment FORCE ROW LEVEL SECURITY;
CREATE POLICY assignment_tenant_isolation ON assignment
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE TRIGGER trg_assignment_updated_at BEFORE UPDATE ON assignment
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON assignment
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- Rows with their signatures (INSERT only: trg_assignment_updated_at keeps
-- updated_at).
INSERT INTO assignment
SELECT (jsonb_populate_record(NULL::assignment,
           q.row_data
           || CASE WHEN c.payload IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('checkout_signature', c.payload) END
           || CASE WHEN r.payload IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('return_signature', r.payload) END)).*
  FROM migration_quarantine q
  LEFT JOIN assignment_signature_legacy c ON c.movement_id = (q.row_data ->> 'id')::uuid AND c.kind = 'checkout'
  LEFT JOIN assignment_signature_legacy r ON r.movement_id = (q.row_data ->> 'id')::uuid AND r.kind = 'return'
 WHERE q.migration = '000266' AND q.source_table = 'assignment'
 ORDER BY q.id;

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'assignment', 'ticket', 'location', 'disposal_record', 'stocktake', 'contract', 'stock_movement'
));

-- Versions of 000242 and 000249.

CREATE OR REPLACE FUNCTION derive_document_link_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    NEW.team_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
              FROM ci c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM asset a WHERE a.id = NEW.entity_id AND a.organization_id = NEW.organization_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id, t.team_id INTO NEW.client_id, NEW.site_id, NEW.team_id
              FROM ticket t WHERE t.id = NEW.entity_id AND t.organization_id = NEW.organization_id;
        WHEN 'assignment' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM assignment s JOIN asset a ON a.id = s.asset_id
             WHERE s.id = NEW.entity_id AND s.organization_id = NEW.organization_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id
              FROM location l WHERE l.id = NEW.entity_id AND l.organization_id = NEW.organization_id;
        WHEN 'disposal_record' THEN
            SELECT d.client_id, d.site_id INTO NEW.client_id, NEW.site_id
              FROM disposal_record d WHERE d.id = NEW.entity_id AND d.organization_id = NEW.organization_id;
        WHEN 'contract' THEN
            SELECT c.client_id INTO NEW.client_id
              FROM contract c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
        WHEN 'stock_movement' THEN
            SELECT m.client_id, m.site_id INTO NEW.client_id, NEW.site_id
              FROM stock_movement m WHERE m.id = NEW.entity_id AND m.organization_id = NEW.organization_id;
        ELSE
            NULL; -- stocktake: org-wide
    END CASE;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION propagate_asset_module_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ticket SET related_asset_id = related_asset_id WHERE related_asset_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'asset' AND entity_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id
     WHERE entity_type = 'assignment'
       AND entity_id IN (SELECT s.id FROM assignment s WHERE s.asset_id = NEW.id);
    UPDATE asset_tag_link SET asset_id = asset_id WHERE asset_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- The moved links hang at their assignment again, the quarantined ones
-- come back (when their document still exists).
UPDATE document_link SET entity_type = 'assignment'
 WHERE entity_type = 'stock_movement'
   AND id IN (SELECT jsonb_array_elements_text(q.row_data -> 'document_link_ids')::uuid
                FROM migration_quarantine q
               WHERE q.migration = '000266' AND q.source_table = 'assignment');

INSERT INTO document_link
SELECT (jsonb_populate_record(NULL::document_link, q.row_data)).*
  FROM migration_quarantine q
 WHERE q.migration = '000266' AND q.source_table = 'document_link'
   AND EXISTS (SELECT 1 FROM document d WHERE d.id = (q.row_data ->> 'document_id')::uuid)
 ORDER BY q.id;

DELETE FROM migration_quarantine WHERE migration = '000266';

DROP TABLE assignment_signature_legacy;
DROP FUNCTION guard_assignment_signature_legacy();
