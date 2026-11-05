-- Migration 000266: drop of the table assignment (WP-843, AST-07).
--
-- Since WP-747 (000257) the assignments are a projection of the ledger
-- stock_movement (assignment_projection, the id of an assignment is that of
-- its assignment movement) and no path writes assignment. What still hangs
-- on the table moves to the ledger or into a safe keeping, then the table
-- goes:
--
-- 1. assignment_signature_legacy keeps the legacy signatures of 000047
--    unchanged (checkout_signature, return_signature: image or object key,
--    signer_name as vault surrogate since WP-656, plaintext only without
--    vault): one row per signature with the assignment movement of the
--    same id (movement_id, kind checkout or return; the return signature
--    belongs to the same assignment, its return movement has its own id),
--    payload and payload_sha256 (hex SHA-256 of payload::text, the
--    canonical jsonb text). The vault subjects of the surrogates stay.
--    stock_movement.signature_hash stays NULL on these movements: SQL
--    cannot hash an image behind an object key (E-51). Row level security
--    on the organization and, with app.client_scope or app.site_scope set,
--    on the visibility of the movement (EXISTS, pattern 000065); ENABLE +
--    FORCE RLS, TRUNCATE guard (000144). A trigger refuses DELETE and every
--    UPDATE but payload := NULL (WP-276; WP-749 widens it); reticora_app
--    holds no DELETE privilege on the table. A row of
--    assignment without a movement of its id (quarantined by 000257:
--    without asset or with a user of another organization) keeps its
--    signatures in its quarantine entry (step 4).
-- 2. Document links: the links with entity_type 'assignment' move to
--    'stock_movement' (entity_id stays, the ids were kept by 000257) and
--    take client and site of the movement. A link whose target has no
--    movement (a row of step 1 without movement or no row at all) cannot
--    move; it is quarantined (source_table 'document_link', reason
--    'dropped: assignment without movement') and deleted.
--    doc_link_entity_type_check loses 'assignment'.
-- 3. derive_document_link_scope() (version of 000242) and
--    propagate_asset_module_scope() (version of 000249) lose their
--    assignment branches; no later migration changed them.
-- 4. Every row of assignment goes to migration_quarantine (source_table
--    'assignment', reason 'dropped: projection', E-26): without the
--    signatures when step 1 kept them, with the ids of the links of step 2
--    (document_link_ids) for the down migration.
-- 5. DROP TABLE assignment.
--
-- All numbers are reported with RAISE NOTICE. Down rebuilds assignment
-- exactly and restores rows, signatures, links, CHECK and functions.

-- ─── 1. Legacy signatures ──────────────────────────────────────────────────────

CREATE TABLE assignment_signature_legacy (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    movement_id     UUID NOT NULL,
    kind            TEXT NOT NULL,
    payload         JSONB,
    payload_sha256  TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT assignment_signature_legacy_kind_check CHECK (kind IN ('checkout', 'return')),
    CONSTRAINT assignment_signature_legacy_sha256_check CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT assignment_signature_legacy_movement_kind_key UNIQUE (movement_id, kind),
    CONSTRAINT assignment_signature_legacy_movement_id_fkey FOREIGN KEY (movement_id, organization_id)
        REFERENCES stock_movement (id, organization_id) ON DELETE RESTRICT
);

CREATE INDEX idx_assignment_signature_legacy_org ON assignment_signature_legacy (organization_id);

ALTER TABLE assignment_signature_legacy ENABLE ROW LEVEL SECURITY;
ALTER TABLE assignment_signature_legacy FORCE ROW LEVEL SECURITY;

-- The movement carries client and site of its booking; a scoped principal
-- sees a signature only when it sees the movement.
CREATE POLICY assignment_signature_legacy_isolation ON assignment_signature_legacy
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND ((NULLIF(current_setting('app.client_scope', true), '') IS NULL
              AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
             OR EXISTS (SELECT 1 FROM stock_movement m WHERE m.id = assignment_signature_legacy.movement_id))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND ((NULLIF(current_setting('app.client_scope', true), '') IS NULL
              AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
             OR EXISTS (SELECT 1 FROM stock_movement m WHERE m.id = assignment_signature_legacy.movement_id))
    );

-- Rows are kept: no DELETE; an UPDATE may only clear payload.
CREATE FUNCTION guard_assignment_signature_legacy() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.payload IS NULL
       AND NEW.id = OLD.id AND NEW.organization_id = OLD.organization_id
       AND NEW.movement_id = OLD.movement_id AND NEW.kind = OLD.kind
       AND NEW.payload_sha256 = OLD.payload_sha256 AND NEW.created_at = OLD.created_at THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'assignment_signature_legacy rows are kept; only payload may be cleared'
        USING ERRCODE = '42501';
END
$$;

CREATE TRIGGER assignment_signature_legacy_guard BEFORE UPDATE OR DELETE ON assignment_signature_legacy
    FOR EACH ROW EXECUTE FUNCTION guard_assignment_signature_legacy();

-- The runtime role does not delete either (pattern audit_log, 000188).
REVOKE DELETE ON assignment_signature_legacy FROM reticora_app;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON assignment_signature_legacy
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 1., 2. and 4. Signatures, links, rows ─────────────────────────────────────

DO $$
DECLARE
    n_signatures bigint;
    n_relinked bigint;
    n_unlinked bigint;
    n_rows bigint;
    n_without_movement bigint;
BEGIN
    -- The rows whose id is a movement of their organization.
    CREATE TEMPORARY TABLE assignment_drop_row ON COMMIT DROP AS
    SELECT s.id, EXISTS (SELECT 1 FROM stock_movement m WHERE m.id = s.id AND m.organization_id = s.organization_id) AS booked
      FROM assignment s;

    INSERT INTO assignment_signature_legacy (organization_id, movement_id, kind, payload, payload_sha256)
    SELECT s.organization_id, s.id, k.kind, k.payload, encode(sha256(convert_to(k.payload::text, 'UTF8')), 'hex')
      FROM assignment s
      JOIN assignment_drop_row r ON r.id = s.id AND r.booked
     CROSS JOIN LATERAL (VALUES ('checkout', s.checkout_signature), ('return', s.return_signature)) AS k (kind, payload)
     WHERE k.payload IS NOT NULL
     ORDER BY s.id, k.kind;
    GET DIAGNOSTICS n_signatures = ROW_COUNT;

    -- 2. The links of a movement move, the others are quarantined.
    CREATE TEMPORARY TABLE assignment_drop_link ON COMMIT DROP AS
    SELECT l.id, l.entity_id,
           EXISTS (SELECT 1 FROM stock_movement m WHERE m.id = l.entity_id AND m.organization_id = l.organization_id) AS movable
      FROM document_link l
     WHERE l.entity_type = 'assignment';

    UPDATE document_link l SET entity_type = 'stock_movement'
      FROM assignment_drop_link d
     WHERE d.id = l.id AND d.movable;
    GET DIAGNOSTICS n_relinked = ROW_COUNT;

    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
    SELECT l.organization_id, 'document_link', to_jsonb(l), 'dropped: assignment without movement', '000266'
      FROM document_link l
      JOIN assignment_drop_link d ON d.id = l.id AND NOT d.movable
     ORDER BY l.id;
    DELETE FROM document_link l USING assignment_drop_link d WHERE d.id = l.id AND NOT d.movable;
    GET DIAGNOSTICS n_unlinked = ROW_COUNT;

    -- 4. Every row, with the moved links.
    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
    SELECT s.organization_id, 'assignment',
           CASE WHEN r.booked THEN to_jsonb(s) - 'checkout_signature' - 'return_signature' ELSE to_jsonb(s) END
           || jsonb_build_object('document_link_ids', COALESCE(
                  (SELECT jsonb_agg(d.id ORDER BY d.id) FROM assignment_drop_link d WHERE d.entity_id = s.id AND d.movable),
                  '[]'::jsonb)),
           'dropped: projection', '000266'
      FROM assignment s
      JOIN assignment_drop_row r ON r.id = s.id
     ORDER BY s.id;
    GET DIAGNOSTICS n_rows = ROW_COUNT;
    SELECT count(*) INTO n_without_movement FROM assignment_drop_row WHERE NOT booked;

    RAISE NOTICE 'assignment drop (000266): % signatures kept in assignment_signature_legacy, % document links moved to their movement, % links without movement quarantined, % assignments quarantined (% without movement, with their signatures)',
        n_signatures, n_relinked, n_unlinked, n_rows, n_without_movement;

    DROP TABLE assignment_drop_link;
    DROP TABLE assignment_drop_row;
END
$$;

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'ticket', 'location', 'disposal_record', 'stocktake', 'contract', 'stock_movement'
));

-- ─── 3. Functions ──────────────────────────────────────────────────────────────

-- Version of 000242 without the branch assignment.
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

-- Version of 000249 without the links of assignments: the links of the
-- movements keep the scope of their booking (append-only).
CREATE OR REPLACE FUNCTION propagate_asset_module_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ticket SET related_asset_id = related_asset_id WHERE related_asset_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'asset' AND entity_id = NEW.id;
    UPDATE asset_tag_link SET asset_id = asset_id WHERE asset_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- ─── 5. Drop ───────────────────────────────────────────────────────────────────

DROP TABLE assignment;
