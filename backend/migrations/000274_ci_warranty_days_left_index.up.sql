-- Migration 000274: the filter on the computed attribute warranty_days_left
-- reads an index (finding of WP-824, ABN-02, NFR-02; SR-04, K22, E-133).
--
-- warranty_days_left (system definition of 000225) is stored in
-- ci.attributes by the refresh job attribute.computed_refresh; the saved
-- views (preset warranty_expiring, VIE-02) compare it as a number. The
-- comparison casts the JSON value to numeric, and numeric comparisons are
-- not leakproof: under the row level policies of the application role no
-- index could serve it, and a page of the filter read every CI of the
-- organization through idx_ci_list_name (TestSavedViewQueryBudget).
--
-- The filter keeps comparing the stored attribute, not warranty_until of
-- the linked asset: the stored value is what the view shows, it may lag
-- behind the date until the next refresh, and a CI may carry it without an
-- asset. E-133, option 2 (pattern of hostname_lc, 000134): the value is a
-- stored generated column of type double precision, compared with the
-- leakproof float8 operators, without a new LEAKPROOF function. It is NULL
-- unless the attribute is a JSON number; the conversion never raises
-- (values from 1e308 on become Infinity, from -1e308 down -Infinity, values
-- closer to zero than 1e-300 become 0), so no write of ci fails on it. The
-- mapping is monotone, so the filter (savedview, numberColumns) uses the
-- column as a coarse index condition, with the operand mapped by the same
-- expression, and keeps the exact numeric comparison next to it: the hits
-- stay those of the numeric comparison for every value.
--
-- Adding a stored column rewrites ci (like 000134 and 000180).
ALTER TABLE ci ADD COLUMN warranty_days_left_num DOUBLE PRECISION GENERATED ALWAYS AS (
    CASE WHEN jsonb_typeof(attributes -> 'warranty_days_left') = 'number' THEN
        CASE WHEN (attributes ->> 'warranty_days_left')::numeric >= 1e308 THEN 'Infinity'::double precision
             WHEN (attributes ->> 'warranty_days_left')::numeric <= -1e308 THEN '-Infinity'::double precision
             WHEN abs((attributes ->> 'warranty_days_left')::numeric) < 1e-300 THEN 0::double precision
             ELSE (attributes ->> 'warranty_days_left')::numeric::double precision
        END
    END) STORED;

COMMENT ON COLUMN ci.warranty_days_left_num IS
    'attributes.warranty_days_left as double precision when it is a JSON number (clamped, never raises), else NULL: index column of the filter on the computed attribute (000274, E-133 option 2).';

CREATE INDEX IF NOT EXISTS idx_ci_warranty_days_left ON ci (organization_id, warranty_days_left_num)
    WHERE deleted_at IS NULL AND warranty_days_left_num IS NOT NULL;
