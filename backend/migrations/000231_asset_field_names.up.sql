-- WP-738 (ast-field-names-db, AST-01, E-41, E-120): the asset columns take
-- the names of AST-01.
--
-- * RENAME asset_tag → inventory_no, purchase_cost → purchase_price (now
--   NUMERIC(14,2)) and warranty_end → warranty_until. NOT NULL and the
--   unique index idx_asset_tag_org go with the column; the index keeps its
--   name, so the constraint mapping of the asset package still applies.
-- * The old names stay as deprecated mirror columns of the type of the new
--   column. The trigger asset_mirror_legacy_columns (BEFORE INSERT OR
--   UPDATE) keeps both sides equal: on insert the new name wins and a
--   missing one is taken from the old name; on update the side that changed
--   wins (the new name when both changed). The asset package (until WP-739)
--   and SQL fixtures that write the old names keep working.
-- * supplier (free text) mirrors supplier.name of the set supplier_id
--   (E-41); without supplier_id the free text stays as written, and clearing
--   supplier_id clears an unchanged free text. Renaming a supplier updates
--   the mirror of its assets (supplier_name_asset_mirror).
-- * status follows lifecycle_state (E-120 (b)): the trigger
--   asset_status_from_lifecycle (BEFORE INSERT OR UPDATE OF lifecycle_state)
--   derives deployed → assigned, repair → maintenance, retired → retired,
--   disposed → disposed and every other state → in_stock; with
--   lifecycle_state NULL status stays as it is. Existing rows with a state
--   get the derived status.
-- * Data migration: the saved views (saved_view.filter_spec: fields of
--   where, sort and columns, bare or as asset.<field>) and the expressions
--   of computed attributes (attribute_definition.computed, e.g. the system
--   definition warranty_days_left of 000225) use the new names.

-- 1. Rename and type.
ALTER TABLE asset RENAME COLUMN asset_tag TO inventory_no;
ALTER TABLE asset RENAME COLUMN purchase_cost TO purchase_price;
ALTER TABLE asset RENAME COLUMN warranty_end TO warranty_until;
ALTER TABLE asset ALTER COLUMN purchase_price TYPE NUMERIC(14,2);

-- 2. Deprecated mirror columns.
ALTER TABLE asset
    ADD COLUMN asset_tag TEXT,
    ADD COLUMN purchase_cost NUMERIC(14,2),
    ADD COLUMN warranty_end DATE;

UPDATE asset SET asset_tag = inventory_no, purchase_cost = purchase_price, warranty_end = warranty_until;

COMMENT ON COLUMN asset.asset_tag IS 'Deprecated mirror of inventory_no (WP-738, E-120 (c)).';
COMMENT ON COLUMN asset.purchase_cost IS 'Deprecated mirror of purchase_price (WP-738, E-120 (c)).';
COMMENT ON COLUMN asset.warranty_end IS 'Deprecated mirror of warranty_until (WP-738, E-120 (c)).';
COMMENT ON COLUMN asset.supplier IS 'Deprecated mirror of supplier.name of supplier_id (E-41, WP-738).';

CREATE FUNCTION asset_mirror_legacy_columns() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    supplier_name TEXT;
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.inventory_no := COALESCE(NEW.inventory_no, NEW.asset_tag);
        NEW.purchase_price := COALESCE(NEW.purchase_price, NEW.purchase_cost);
        NEW.warranty_until := COALESCE(NEW.warranty_until, NEW.warranty_end);
    ELSE
        IF NEW.inventory_no IS NOT DISTINCT FROM OLD.inventory_no AND NEW.asset_tag IS DISTINCT FROM OLD.asset_tag THEN
            NEW.inventory_no := NEW.asset_tag;
        END IF;
        IF NEW.purchase_price IS NOT DISTINCT FROM OLD.purchase_price AND NEW.purchase_cost IS DISTINCT FROM OLD.purchase_cost THEN
            NEW.purchase_price := NEW.purchase_cost;
        END IF;
        IF NEW.warranty_until IS NOT DISTINCT FROM OLD.warranty_until AND NEW.warranty_end IS DISTINCT FROM OLD.warranty_end THEN
            NEW.warranty_until := NEW.warranty_end;
        END IF;
        IF NEW.supplier_id IS NULL AND OLD.supplier_id IS NOT NULL AND NEW.supplier IS NOT DISTINCT FROM OLD.supplier THEN
            NEW.supplier := NULL;
        END IF;
    END IF;
    NEW.asset_tag := NEW.inventory_no;
    NEW.purchase_cost := NEW.purchase_price;
    NEW.warranty_end := NEW.warranty_until;
    IF NEW.supplier_id IS NOT NULL THEN
        SELECT s.name INTO supplier_name FROM supplier s
        WHERE s.id = NEW.supplier_id AND s.organization_id = NEW.organization_id;
        IF FOUND THEN
            NEW.supplier := supplier_name;
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER asset_mirror_legacy_columns
    BEFORE INSERT OR UPDATE ON asset
    FOR EACH ROW EXECUTE FUNCTION asset_mirror_legacy_columns();

-- The free text of assets with a supplier record is its name.
UPDATE asset a SET supplier = s.name
FROM supplier s
WHERE s.id = a.supplier_id AND s.organization_id = a.organization_id AND a.supplier IS DISTINCT FROM s.name;

CREATE FUNCTION supplier_name_asset_mirror() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE asset SET supplier = NEW.name
    WHERE supplier_id = NEW.id AND organization_id = NEW.organization_id AND supplier IS DISTINCT FROM NEW.name;
    RETURN NULL;
END
$$;

CREATE TRIGGER supplier_name_asset_mirror
    AFTER UPDATE OF name ON supplier
    FOR EACH ROW WHEN (OLD.name IS DISTINCT FROM NEW.name)
    EXECUTE FUNCTION supplier_name_asset_mirror();

-- 3. status from lifecycle_state (E-120 (b)).
CREATE FUNCTION asset_status_from_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.lifecycle_state IS NOT NULL THEN
        NEW.status := CASE NEW.lifecycle_state
                          WHEN 'deployed' THEN 'assigned'
                          WHEN 'repair' THEN 'maintenance'
                          WHEN 'retired' THEN 'retired'
                          WHEN 'disposed' THEN 'disposed'
                          ELSE 'in_stock'
                      END;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER asset_status_from_lifecycle
    BEFORE INSERT OR UPDATE OF lifecycle_state ON asset
    FOR EACH ROW EXECUTE FUNCTION asset_status_from_lifecycle();

UPDATE asset SET lifecycle_state = lifecycle_state
WHERE lifecycle_state IS NOT NULL
  AND status IS DISTINCT FROM CASE lifecycle_state
                                  WHEN 'deployed' THEN 'assigned'
                                  WHEN 'repair' THEN 'maintenance'
                                  WHEN 'retired' THEN 'retired'
                                  WHEN 'disposed' THEN 'disposed'
                                  ELSE 'in_stock'
                              END;

-- 4. Saved views and computed expressions with the new names. The helper
--    renames every member "field" of an object (recursively) and is dropped
--    again.
CREATE FUNCTION wp738_rename_fields(node jsonb, renames jsonb) RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    CASE jsonb_typeof(node)
        WHEN 'object' THEN
            RETURN (SELECT COALESCE(jsonb_object_agg(k,
                        CASE WHEN k = 'field' AND jsonb_typeof(v) = 'string' AND renames ? (v #>> '{}')
                             THEN renames -> (v #>> '{}')
                             ELSE wp738_rename_fields(v, renames)
                        END), '{}'::jsonb)
                    FROM jsonb_each(node) AS e(k, v));
        WHEN 'array' THEN
            RETURN (SELECT COALESCE(jsonb_agg(wp738_rename_fields(v, renames) ORDER BY o), '[]'::jsonb)
                    FROM jsonb_array_elements(node) WITH ORDINALITY AS e(v, o));
        ELSE
            RETURN node;
    END CASE;
END
$$;

CREATE FUNCTION wp738_rename_spec(spec jsonb, renames jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT spec
        || CASE WHEN spec ? 'where' THEN jsonb_build_object('where', wp738_rename_fields(spec -> 'where', renames)) ELSE '{}'::jsonb END
        || CASE WHEN jsonb_typeof(spec -> 'sort') = 'array'
                THEN jsonb_build_object('sort', wp738_rename_fields(spec -> 'sort', renames)) ELSE '{}'::jsonb END
        || CASE WHEN jsonb_typeof(spec -> 'columns') = 'array'
                THEN jsonb_build_object('columns', (
                    SELECT COALESCE(jsonb_agg(CASE WHEN jsonb_typeof(c) = 'string' AND renames ? (c #>> '{}')
                                                   THEN renames -> (c #>> '{}') ELSE c END ORDER BY o), '[]'::jsonb)
                    FROM jsonb_array_elements(spec -> 'columns') WITH ORDINALITY AS e(c, o)))
                ELSE '{}'::jsonb END
$$;

UPDATE saved_view v SET filter_spec = r.spec
FROM (SELECT id, wp738_rename_spec(filter_spec, '{
        "asset_tag": "inventory_no", "purchase_cost": "purchase_price", "warranty_end": "warranty_until",
        "asset.asset_tag": "asset.inventory_no", "asset.purchase_cost": "asset.purchase_price",
        "asset.warranty_end": "asset.warranty_until"}'::jsonb) AS spec
      FROM saved_view) AS r
WHERE v.id = r.id AND v.filter_spec IS DISTINCT FROM r.spec;

UPDATE attribute_definition d SET computed = r.expr
FROM (SELECT id, wp738_rename_fields(computed, '{
        "asset.asset_tag": "asset.inventory_no", "asset.purchase_cost": "asset.purchase_price",
        "asset.warranty_end": "asset.warranty_until"}'::jsonb) AS expr
      FROM attribute_definition WHERE computed IS NOT NULL) AS r
WHERE d.id = r.id AND d.computed IS DISTINCT FROM r.expr;

DROP FUNCTION wp738_rename_spec(jsonb, jsonb);
DROP FUNCTION wp738_rename_fields(jsonb, jsonb);
