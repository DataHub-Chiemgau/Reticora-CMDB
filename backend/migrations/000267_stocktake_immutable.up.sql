-- Migration 000267: the result of a completed or cancelled stocktake is
-- immutable (INV-01, WP-761). No schema change, no data migration.
--
-- reject_completed_stocktake_mutation is a BEFORE trigger on stocktake
-- (UPDATE, DELETE), stock_scan and stocktake_item (INSERT, UPDATE,
-- DELETE). It reads OLD.status of a stocktake, or the status of the
-- stocktake of a scan or item, and refuses the change with 23514 on
-- stocktake_immutable once the stocktake is completed or cancelled. The
-- completing UPDATE itself passes, OLD.status is in_progress then. Two
-- exceptions: an UPDATE of stock_scan that only sets ci_id to NULL
-- (jobs.HardDeleteCI, CI-05/JOB-02), and an UPDATE of any of the three
-- that changes only client_id and/or site_id, the scope refresh after a
-- location changed its client or site (location_refresh_stocktake,
-- stocktake_refresh_items, stocktake_refresh_scans); result, quantities,
-- totals, status and timestamps stay locked. trg_stocktake_updated_at
-- (000036) runs after this trigger and sets updated_at of a refreshed
-- stocktake. This keeps the append-only promise of
-- stock_scan from 000036. The application checks the status under its lock
-- before (409); the trigger is the last guard, its refusal is 409 as well.
-- A delete of a stocktake that is not completed cascades to its scans and
-- items: the stocktake row is gone when their triggers run, so they pass.
-- The status is read with the client and site scope widened
-- (app_scope_widen, 000139), so a scan derived from its object sees the
-- org-wide stocktake of an earlier release. The test fixtures remove
-- completed stocktakes under session_replication_role = replica
-- (scopetest.appendOnlyTables).

CREATE FUNCTION reject_completed_stocktake_mutation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    st_status TEXT;
    ids UUID[];
BEGIN
    -- The scope refresh (location_refresh_stocktake, stocktake_refresh_items,
    -- stocktake_refresh_scans) re-derives only client_id and site_id after a
    -- location changed its client or site; the derivation triggers run
    -- before this one (trigger names in order), so NEW carries the new
    -- scope. Every other column stays as it was.
    IF TG_OP = 'UPDATE'
       AND (to_jsonb(NEW) - 'client_id' - 'site_id') = (to_jsonb(OLD) - 'client_id' - 'site_id') THEN
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'stocktake' THEN
        st_status := OLD.status;
    ELSE
        -- Nested: OLD.ci_id is no field of stocktake_item, and an
        -- expression is planned as a whole.
        IF TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'stock_scan' THEN
            IF OLD.ci_id IS NOT NULL AND NEW.ci_id IS NULL
               AND (to_jsonb(NEW) - 'ci_id') = (to_jsonb(OLD) - 'ci_id') THEN
                RETURN NEW;
            END IF;
        END IF;
        IF TG_OP = 'INSERT' THEN
            ids := ARRAY[NEW.stocktake_id];
        ELSIF TG_OP = 'DELETE' THEN
            ids := ARRAY[OLD.stocktake_id];
        ELSE
            ids := ARRAY[OLD.stocktake_id, NEW.stocktake_id];
        END IF;
        previous := app_scope_widen();
        SELECT s.status INTO st_status
          FROM stocktake s
         WHERE s.id = ANY (ids) AND s.status IN ('completed', 'cancelled')
         LIMIT 1;
        PERFORM app_scope_restore(previous);
    END IF;
    IF st_status IN ('completed', 'cancelled') THEN
        RAISE EXCEPTION 'the stocktake is % and its result is immutable', st_status
            USING ERRCODE = '23514', CONSTRAINT = 'stocktake_immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER stocktake_immutable BEFORE UPDATE OR DELETE ON stocktake
    FOR EACH ROW EXECUTE FUNCTION reject_completed_stocktake_mutation();
CREATE TRIGGER stock_scan_immutable BEFORE INSERT OR UPDATE OR DELETE ON stock_scan
    FOR EACH ROW EXECUTE FUNCTION reject_completed_stocktake_mutation();
CREATE TRIGGER stocktake_item_immutable BEFORE INSERT OR UPDATE OR DELETE ON stocktake_item
    FOR EACH ROW EXECUTE FUNCTION reject_completed_stocktake_mutation();
