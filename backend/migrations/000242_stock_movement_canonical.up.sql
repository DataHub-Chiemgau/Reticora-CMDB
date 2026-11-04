-- Migration 000242: the canonical inventory ledger stock_movement (WP-744,
-- STK-04, AST-07).
--
-- Until now there were two ledgers: stock_movement (000045) for consumables,
-- with direction in/out and reference, mutable and cascading with its
-- consumable, and the append-only asset_movement (000055) for assets and
-- quantity items, with movement types outside STK-04 and without scope
-- columns. Both filtered by organization only.
--
-- 1. The former stock_movement becomes stock_movement_legacy (with its
--    primary key and idx_stock_movement_consumable) and is dropped after
--    the data migration.
-- 2. quantity_item gets UNIQUE (id, organization_id) for the composite
--    reference (K19); location, consumable (000194), asset (000056),
--    app_user (000177) and team (000150) have theirs.
-- 3. The new tenant table stock_movement: one object column matching
--    item_kind (quantity_item only as a transition until E-40), the movement
--    types of STK-04, quantity NULL for assets and positive otherwise, at
--    most one assignee (user or team). actor_id is a user or service
--    account id without foreign key like audit_log.actor_id; user and team
--    ids are surrogates (WP-241, SEC-05), reason and notes carry no names.
--    signature_hash stays empty until WP-276. Every reference to another
--    tenant table carries the organization (K19), ON DELETE RESTRICT; the
--    organization cascades like asset_movement. ticket_id, order_id,
--    workflow_run_id and document_id have no foreign key (the objects can
--    be deleted), client_id and site_id are derived and have none either
--    (pattern 000061/000064).
-- 4. stock_movement_append_only refuses every UPDATE and DELETE.
--    stock_movement_derive_scope (BEFORE INSERT) overwrites client_id and
--    site_id whatever the writer passes: the client of the asset,
--    consumable or quantity item, the site of to_location_id, else of
--    from_location_id, else the site of the object (asset and quantity_item
--    since 000151; consumable has no location until WP-283). It runs under
--    the writer's row level security: an object outside the scope derives
--    NULL and WITH CHECK refuses the row for scoped principals (E-09). The
--    scope is frozen at booking time; nothing propagates it.
-- 5. ENABLE + FORCE RLS, TRUNCATE guard (000144) and stock_movement_isolation
--    with organization, client and site predicate (pattern 000151, E-09):
--    rows without client or site are readable for everybody of the
--    organization but written only by principals without that restriction.
--    Movements without a derivable site (consumables until WP-283, objects
--    without location) are therefore no longer written by site-restricted
--    principals; the former stock_movement checked the organization only.
--    The restrictive policies stock_movement_scoped_update and
--    stock_movement_scoped_delete keep the write scope for UPDATE and DELETE
--    (pattern 000151) behind the append-only trigger.
-- 6. Data: asset_movement is copied with its ids (occurred_at = created_at),
--    types mapped to STK-04 (warehouse_transfer, bin_transfer → transfer,
--    reservation_release → release, repair_transfer → repair, unknown →
--    correction with the original in reason). Asset rows lose a quantity
--    (appended to reason), rows with an asset and a quantity item keep the
--    object of item_kind, quantity item rows without a positive quantity and
--    rows referencing an object or location of another organization go to
--    migration_quarantine. Assignment movements with the notes "assigned
--    to <uuid>" / "transferred to <uuid>" get assignee_user_id when the uuid
--    is a user of the organization. asset_movement itself stays unchanged
--    (dropped by WP-842). stock_movement_legacy is copied with its ids as
--    consumable movements: in → receipt, out → retrieval, reference →
--    notes.
-- 7. The history of assignment: one assignment movement per row
--    (occurred_at = assigned_at, assignee_user_id = assigned_to, actor_id =
--    assigned_by) and, when returned, a return movement (occurred_at =
--    returned_at, reason = return_condition), both with notes
--    'assignment:<id>' and created_at = occurred_at. A row with only ci_id
--    takes the asset of the CI; without an asset it stays in assignment
--    only. A movement is skipped when asset_movement holds the same one
--    (same asset and type, the notes of assignment/handler.go, at most 5 s
--    apart).
-- 8. document_link admits stock_movement; derive_document_link_scope takes
--    client and site of the movement (basis: the version of 000238).

-- ─── 1. Former ledger ──────────────────────────────────────────────────────────

ALTER TABLE stock_movement RENAME TO stock_movement_legacy;
ALTER INDEX stock_movement_pkey RENAME TO stock_movement_legacy_pkey;
ALTER INDEX idx_stock_movement_consumable RENAME TO idx_stock_movement_legacy_consumable;

-- ─── 2. Keys ───────────────────────────────────────────────────────────────────

ALTER TABLE quantity_item ADD CONSTRAINT quantity_item_id_organization_key UNIQUE (id, organization_id);

-- ─── 3. Table ──────────────────────────────────────────────────────────────────

-- Every constraint is named: stock_movement_legacy still holds constraints
-- of the same names, generated names would get a suffix.

CREATE TABLE stock_movement (
    id               UUID CONSTRAINT stock_movement_pkey PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  UUID NOT NULL CONSTRAINT stock_movement_organization_id_fkey
        REFERENCES organization(id) ON DELETE CASCADE,
    client_id        UUID,
    site_id          UUID,
    item_kind        TEXT NOT NULL,
    asset_id         UUID,
    consumable_id    UUID,
    quantity_item_id UUID,
    movement_type    TEXT NOT NULL,
    from_location_id UUID,
    to_location_id   UUID,
    quantity         NUMERIC(12,2),
    actor_id         UUID,
    assignee_user_id UUID,
    assignee_team_id UUID,
    occurred_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason           TEXT,
    notes            TEXT,
    ticket_id        UUID,
    order_id         UUID,
    workflow_run_id  UUID,
    document_id      UUID,
    signature_hash   TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT stock_movement_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT stock_movement_item_kind_check CHECK (item_kind IN ('asset', 'consumable', 'quantity_item')),
    CONSTRAINT stock_movement_object_check CHECK (num_nonnulls(asset_id, consumable_id, quantity_item_id) = 1
        AND CASE item_kind
                WHEN 'asset' THEN asset_id IS NOT NULL
                WHEN 'consumable' THEN consumable_id IS NOT NULL
                ELSE quantity_item_id IS NOT NULL
            END),
    CONSTRAINT stock_movement_movement_type_check CHECK (movement_type IN (
        'receipt', 'transfer', 'assignment', 'return', 'deployment', 'retrieval',
        'reservation', 'release', 'repair', 'disposal', 'correction'
    )),
    CONSTRAINT stock_movement_quantity_check CHECK (CASE item_kind
            WHEN 'asset' THEN quantity IS NULL
            ELSE quantity IS NOT NULL AND quantity > 0
        END),
    CONSTRAINT stock_movement_assignee_check CHECK (num_nonnulls(assignee_user_id, assignee_team_id) <= 1),
    CONSTRAINT stock_movement_asset_id_fkey FOREIGN KEY (asset_id, organization_id)
        REFERENCES asset (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stock_movement_consumable_id_fkey FOREIGN KEY (consumable_id, organization_id)
        REFERENCES consumable (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stock_movement_quantity_item_id_fkey FOREIGN KEY (quantity_item_id, organization_id)
        REFERENCES quantity_item (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stock_movement_from_location_id_fkey FOREIGN KEY (from_location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stock_movement_to_location_id_fkey FOREIGN KEY (to_location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stock_movement_assignee_user_id_fkey FOREIGN KEY (assignee_user_id, organization_id)
        REFERENCES app_user (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT stock_movement_assignee_team_id_fkey FOREIGN KEY (assignee_team_id, organization_id)
        REFERENCES team (id, organization_id) ON DELETE RESTRICT
);

CREATE INDEX idx_stock_movement_org ON stock_movement (organization_id, occurred_at DESC);
CREATE INDEX idx_stock_movement_asset ON stock_movement (asset_id, occurred_at DESC) WHERE asset_id IS NOT NULL;
CREATE INDEX idx_stock_movement_consumable ON stock_movement (consumable_id, occurred_at DESC) WHERE consumable_id IS NOT NULL;
CREATE INDEX idx_stock_movement_quantity_item ON stock_movement (quantity_item_id, occurred_at DESC) WHERE quantity_item_id IS NOT NULL;
CREATE INDEX idx_stock_movement_from_location ON stock_movement (from_location_id) WHERE from_location_id IS NOT NULL;
CREATE INDEX idx_stock_movement_to_location ON stock_movement (to_location_id) WHERE to_location_id IS NOT NULL;
CREATE INDEX idx_stock_movement_type ON stock_movement (organization_id, movement_type);

-- ─── 4. Triggers ───────────────────────────────────────────────────────────────

CREATE FUNCTION reject_stock_movement_mutation() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'stock_movement rows are append-only';
END
$$;

CREATE TRIGGER stock_movement_append_only BEFORE UPDATE OR DELETE ON stock_movement
    FOR EACH ROW EXECUTE FUNCTION reject_stock_movement_mutation();

-- derive_stock_movement_scope reads the object and the locations under the
-- writer's row level security (no app_scope_widen): what the writer cannot
-- see derives NULL.
CREATE FUNCTION derive_stock_movement_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    object_site UUID;
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.item_kind
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, object_site
              FROM asset a WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
        WHEN 'consumable' THEN
            SELECT c.client_id INTO NEW.client_id
              FROM consumable c WHERE c.id = NEW.consumable_id AND c.organization_id = NEW.organization_id;
        WHEN 'quantity_item' THEN
            SELECT q.client_id, q.site_id INTO NEW.client_id, object_site
              FROM quantity_item q WHERE q.id = NEW.quantity_item_id AND q.organization_id = NEW.organization_id;
        ELSE
            NULL; -- refused by stock_movement_item_kind_check
    END CASE;
    IF NEW.to_location_id IS NOT NULL THEN
        SELECT l.site_id INTO NEW.site_id
          FROM location l WHERE l.id = NEW.to_location_id AND l.organization_id = NEW.organization_id;
    ELSIF NEW.from_location_id IS NOT NULL THEN
        SELECT l.site_id INTO NEW.site_id
          FROM location l WHERE l.id = NEW.from_location_id AND l.organization_id = NEW.organization_id;
    ELSE
        NEW.site_id := object_site;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER stock_movement_derive_scope BEFORE INSERT ON stock_movement
    FOR EACH ROW EXECUTE FUNCTION derive_stock_movement_scope();

-- ─── 5. Row level security ─────────────────────────────────────────────────────

ALTER TABLE stock_movement ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_movement FORCE ROW LEVEL SECURITY;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON stock_movement
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE POLICY stock_movement_isolation ON stock_movement
    USING (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

-- UPDATE and DELETE are refused by stock_movement_append_only; the
-- restrictive policies keep the write scope of 000151 for them as well, so
-- the policies alone never let a scoped principal touch a row outside it.
CREATE POLICY stock_movement_scoped_update ON stock_movement AS RESTRICTIVE FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY stock_movement_scoped_delete ON stock_movement AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

-- ─── 6. and 7. Data ────────────────────────────────────────────────────────────

-- The rows of asset_movement with the kept object (item_kind decides when
-- both are set), the mapped type and the problem that keeps a row out of
-- the ledger. A temporary view; dropped below.
CREATE TEMPORARY VIEW stock_movement_import AS
SELECT m.*,
       to_jsonb(m) AS row_data,
       CASE WHEN m.movement_type IN ('receipt', 'transfer', 'assignment', 'return', 'deployment', 'retrieval',
                                     'reservation', 'release', 'repair', 'disposal', 'correction')
                THEN m.movement_type
            WHEN m.movement_type IN ('warehouse_transfer', 'bin_transfer') THEN 'transfer'
            WHEN m.movement_type = 'reservation_release' THEN 'release'
            WHEN m.movement_type = 'repair_transfer' THEN 'repair'
            ELSE 'correction'
       END AS canonical_type,
       CASE WHEN m.asset_id IS NOT NULL AND m.quantity_item_id IS NOT NULL THEN m.item_kind
            WHEN m.asset_id IS NOT NULL THEN 'asset'
            ELSE 'quantity_item'
       END AS kind
  FROM asset_movement m;

CREATE TEMPORARY VIEW stock_movement_import_checked AS
SELECT i.*,
       CASE WHEN i.kind = 'quantity_item' AND (i.quantity IS NULL OR i.quantity <= 0)
                THEN 'movement_invalid_quantity'
            WHEN (i.kind = 'asset' AND NOT EXISTS (SELECT 1 FROM asset a
                     WHERE a.id = i.asset_id AND a.organization_id = i.organization_id))
              OR (i.kind = 'quantity_item' AND NOT EXISTS (SELECT 1 FROM quantity_item q
                     WHERE q.id = i.quantity_item_id AND q.organization_id = i.organization_id))
              OR (i.from_location_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM location l
                     WHERE l.id = i.from_location_id AND l.organization_id = i.organization_id))
              OR (i.to_location_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM location l
                     WHERE l.id = i.to_location_id AND l.organization_id = i.organization_id))
                THEN 'movement_foreign_reference'
       END AS problem
  FROM stock_movement_import i;

DO $$
DECLARE
    corrected bigint;
    cleaned_quantity bigint;
    cleaned_object bigint;
    quarantined bigint;
    quarantined_legacy bigint;
    duplicates bigint;
    without_asset bigint;
    imported bigint;
BEGIN
    SELECT count(*) FILTER (WHERE canonical_type = 'correction' AND movement_type <> 'correction'),
           count(*) FILTER (WHERE kind = 'asset' AND quantity IS NOT NULL),
           count(*) FILTER (WHERE asset_id IS NOT NULL AND quantity_item_id IS NOT NULL)
      INTO corrected, cleaned_quantity, cleaned_object
      FROM stock_movement_import_checked WHERE problem IS NULL;

    -- 6a. asset_movement.
    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
    SELECT organization_id, 'asset_movement', row_data, problem, '000242'
      FROM stock_movement_import_checked WHERE problem IS NOT NULL;
    GET DIAGNOSTICS quarantined = ROW_COUNT;

    INSERT INTO stock_movement (
        id, organization_id, item_kind, asset_id, quantity_item_id, movement_type,
        from_location_id, to_location_id, quantity, actor_id, assignee_user_id,
        occurred_at, reason, notes, ticket_id, order_id, workflow_run_id, document_id, created_at)
    SELECT i.id, i.organization_id, i.kind,
           CASE WHEN i.kind = 'asset' THEN i.asset_id END,
           CASE WHEN i.kind = 'quantity_item' THEN i.quantity_item_id END,
           i.canonical_type, i.from_location_id, i.to_location_id,
           CASE WHEN i.kind = 'asset' THEN NULL ELSE i.quantity END,
           i.actor_id,
           CASE WHEN i.canonical_type = 'assignment' THEN (
               SELECT u.id FROM app_user u
                WHERE u.organization_id = i.organization_id
                  AND u.id::text = lower(substring(i.notes FROM
                      '^(?:assigned|transferred) to ([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$')))
           END,
           i.created_at,
           NULLIF(concat_ws('; ', NULLIF(i.reason, ''),
                  CASE WHEN i.canonical_type = 'correction' AND i.movement_type <> 'correction'
                       THEN 'movement_type ' || i.movement_type END,
                  CASE WHEN i.kind = 'asset' AND i.quantity IS NOT NULL
                       THEN 'quantity ' || i.quantity::text END), ''),
           i.notes, i.ticket_id, i.order_id, i.workflow_run_id, i.document_id, i.created_at
      FROM stock_movement_import_checked i
     WHERE i.problem IS NULL;
    GET DIAGNOSTICS imported = ROW_COUNT;

    -- 6b. The former stock_movement.
    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
    SELECT s.organization_id, 'stock_movement', to_jsonb(s), 'movement_foreign_reference', '000242'
      FROM stock_movement_legacy s
     WHERE NOT EXISTS (SELECT 1 FROM consumable c WHERE c.id = s.consumable_id AND c.organization_id = s.organization_id);
    GET DIAGNOSTICS quarantined_legacy = ROW_COUNT;
    quarantined := quarantined + quarantined_legacy;

    INSERT INTO stock_movement (
        id, organization_id, item_kind, consumable_id, movement_type, quantity,
        actor_id, occurred_at, reason, notes, created_at)
    SELECT s.id, s.organization_id, 'consumable', s.consumable_id,
           CASE s.direction WHEN 'in' THEN 'receipt' ELSE 'retrieval' END,
           s.quantity, s.actor_id, s.created_at, s.reason, s.reference, s.created_at
      FROM stock_movement_legacy s
     WHERE EXISTS (SELECT 1 FROM consumable c WHERE c.id = s.consumable_id AND c.organization_id = s.organization_id);

    -- 7. The history of assignment.
    CREATE TEMPORARY TABLE stock_movement_assignment ON COMMIT DROP AS
    SELECT s.id, s.organization_id, s.assigned_to, s.assigned_by, s.assigned_at,
           s.returned_at, s.return_condition,
           (SELECT a.id FROM asset a
             WHERE a.organization_id = s.organization_id
               AND a.id = COALESCE(s.asset_id,
                   (SELECT c.asset_id FROM ci c WHERE c.id = s.ci_id AND c.organization_id = s.organization_id))) AS asset_id,
           (SELECT u.id FROM app_user u WHERE u.id = s.assigned_to AND u.organization_id = s.organization_id) AS assignee
      FROM assignment s;

    SELECT count(*) INTO without_asset FROM stock_movement_assignment WHERE asset_id IS NULL;

    WITH candidate AS (
        SELECT s.organization_id, s.asset_id, 'assignment' AS movement_type, s.assigned_by AS actor_id,
               s.assignee, s.assigned_at AS occurred_at, NULL::text AS reason, 'assignment:' || s.id AS notes,
               EXISTS (SELECT 1 FROM asset_movement m
                        WHERE m.organization_id = s.organization_id AND m.asset_id = s.asset_id
                          AND m.movement_type = 'assignment'
                          AND m.notes IN ('assigned to ' || s.assigned_to, 'transferred to ' || s.assigned_to)
                          AND abs(extract(epoch FROM m.created_at - s.assigned_at)) <= 5) AS duplicate
          FROM stock_movement_assignment s WHERE s.asset_id IS NOT NULL
        UNION ALL
        SELECT s.organization_id, s.asset_id, 'return', NULL, NULL, s.returned_at, s.return_condition,
               'assignment:' || s.id,
               EXISTS (SELECT 1 FROM asset_movement m
                        WHERE m.organization_id = s.organization_id AND m.asset_id = s.asset_id
                          AND m.movement_type = 'return' AND m.notes = 'assignment returned'
                          AND abs(extract(epoch FROM m.created_at - s.returned_at)) <= 5)
          FROM stock_movement_assignment s WHERE s.asset_id IS NOT NULL AND s.returned_at IS NOT NULL
    ), inserted AS (
        INSERT INTO stock_movement (
            organization_id, item_kind, asset_id, movement_type, actor_id, assignee_user_id,
            occurred_at, reason, notes, created_at)
        SELECT c.organization_id, 'asset', c.asset_id, c.movement_type, c.actor_id, c.assignee,
               c.occurred_at, c.reason, c.notes, c.occurred_at
          FROM candidate c WHERE NOT c.duplicate
        RETURNING 1
    )
    SELECT count(*) FILTER (WHERE duplicate) INTO duplicates FROM candidate;

    DROP TABLE stock_movement_assignment;

    RAISE NOTICE 'stock_movement (000242): % asset movements imported, % types mapped to correction, % asset quantities and % second object columns cleared, % rows quarantined, % assignment movements skipped as duplicates, % assignments without asset',
        imported, corrected, cleaned_quantity, cleaned_object, quarantined, duplicates, without_asset;
END
$$;

DROP VIEW stock_movement_import_checked;
DROP VIEW stock_movement_import;
DROP TABLE stock_movement_legacy;

-- ─── 8. Document links ─────────────────────────────────────────────────────────

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'assignment', 'ticket', 'location', 'disposal_record', 'stocktake', 'contract', 'stock_movement'
));

-- derive_document_link_scope runs under the RLS of the writer: a target
-- outside its scope derives NULL and is then rejected by WITH CHECK for
-- scoped principals (E-09). stocktake links stay org-wide. A movement keeps
-- the scope of its booking (append-only), so its links need no propagation.
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
