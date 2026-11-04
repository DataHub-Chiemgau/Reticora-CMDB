-- WP-738: back to the old asset column names. purchase_cost returns to
-- NUMERIC(12,2), which requires amounts within the old range. The saved
-- views and computed expressions get the old names again; the derived
-- status and the supplier mirror stay (data, no schema).

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
        "inventory_no": "asset_tag", "purchase_price": "purchase_cost", "warranty_until": "warranty_end",
        "asset.inventory_no": "asset.asset_tag", "asset.purchase_price": "asset.purchase_cost",
        "asset.warranty_until": "asset.warranty_end"}'::jsonb) AS spec
      FROM saved_view) AS r
WHERE v.id = r.id AND v.filter_spec IS DISTINCT FROM r.spec;

UPDATE attribute_definition d SET computed = r.expr
FROM (SELECT id, wp738_rename_fields(computed, '{
        "asset.inventory_no": "asset.asset_tag", "asset.purchase_price": "asset.purchase_cost",
        "asset.warranty_until": "asset.warranty_end"}'::jsonb) AS expr
      FROM attribute_definition WHERE computed IS NOT NULL) AS r
WHERE d.id = r.id AND d.computed IS DISTINCT FROM r.expr;

DROP FUNCTION wp738_rename_spec(jsonb, jsonb);
DROP FUNCTION wp738_rename_fields(jsonb, jsonb);

DROP TRIGGER asset_status_from_lifecycle ON asset;
DROP FUNCTION asset_status_from_lifecycle();
DROP TRIGGER supplier_name_asset_mirror ON supplier;
DROP FUNCTION supplier_name_asset_mirror();
DROP TRIGGER asset_mirror_legacy_columns ON asset;
DROP FUNCTION asset_mirror_legacy_columns();

COMMENT ON COLUMN asset.supplier IS NULL;

ALTER TABLE asset
    DROP COLUMN asset_tag,
    DROP COLUMN purchase_cost,
    DROP COLUMN warranty_end;

ALTER TABLE asset ALTER COLUMN purchase_price TYPE NUMERIC(12,2);
ALTER TABLE asset RENAME COLUMN warranty_until TO warranty_end;
ALTER TABLE asset RENAME COLUMN purchase_price TO purchase_cost;
ALTER TABLE asset RENAME COLUMN inventory_no TO asset_tag;
