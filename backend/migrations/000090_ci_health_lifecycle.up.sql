-- WP-080 (DB-05, E-36):
-- * ci.lifecycle_state is the software lifecycle and exists only for CI
--   types with a lifecycle model (ci_type.lifecycle_definition_id); the
--   value must be a state of that model. Values of CIs whose type has no
--   model move to migration_quarantine.
-- * ci.health is read-only and computed from the monitoring state by
--   ci_refresh_health (latest sample per CI and metric in the window against
--   the enabled alert rules); the findings share is not G1 scope (E-36).

WITH moved AS (
    UPDATE ci c SET lifecycle_state = NULL
    FROM ci_type t
    WHERE t.id = c.ci_type_id AND c.lifecycle_state IS NOT NULL
      AND (t.lifecycle_definition_id IS NULL OR NOT EXISTS (
          SELECT 1 FROM lifecycle_state s
          WHERE s.definition_id = t.lifecycle_definition_id AND s.key = c.lifecycle_state))
    RETURNING c.organization_id, c.id, (SELECT lifecycle_state FROM ci o WHERE o.id = c.id) AS old_state
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'ci', jsonb_build_object('id', id, 'lifecycle_state', old_state),
       'lifecycle_state without a lifecycle model of the CI type (DB-05)', '000090'
FROM moved;

CREATE FUNCTION ci_check_lifecycle_model() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.lifecycle_state IS NULL THEN
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM ci_type t JOIN lifecycle_state s ON s.definition_id = t.lifecycle_definition_id
        WHERE t.id = NEW.ci_type_id AND s.key = NEW.lifecycle_state
    ) THEN
        RAISE EXCEPTION 'lifecycle state % is not a state of the lifecycle model of the type of CI %',
            NEW.lifecycle_state, NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_lifecycle_requires_model';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ci_lifecycle_model BEFORE INSERT OR UPDATE OF lifecycle_state, ci_type_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_check_lifecycle_model();

ALTER TABLE ci ADD COLUMN health text NOT NULL DEFAULT 'unknown'
    CONSTRAINT ci_health_check CHECK (health IN ('ok', 'warning', 'critical', 'unknown'));
ALTER TABLE ci ADD COLUMN health_updated_at timestamptz;

-- Only ci_refresh_health writes the health (read-only for every writer).
CREATE FUNCTION ci_health_read_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.health IS DISTINCT FROM OLD.health
       AND COALESCE(current_setting('reticora.health_refresh', true), '') <> 'on' THEN
        RAISE EXCEPTION 'the health of CI % is computed from monitoring', NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_health_read_only';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ci_health_read_only BEFORE UPDATE OF health ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_health_read_only();

-- Computes the health of the organization's CIs (app.org_id): critical or
-- warning when the latest sample of a metric breaches an enabled alert rule
-- of that severity, ok when samples exist without a breach, unknown when the
-- CI has no sample in the window. Returns the number of changed CIs.
CREATE FUNCTION ci_refresh_health(p_window interval) RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    changed integer;
BEGIN
    PERFORM set_config('reticora.health_refresh', 'on', true);
    WITH latest AS (
        SELECT DISTINCT ON (ci_id, metric_name) ci_id, metric_name, value
        FROM metric_sample_v
        WHERE time > now() - p_window
        ORDER BY ci_id, metric_name, time DESC
    ), level AS (
        SELECT l.ci_id,
               max(CASE r.severity WHEN 'critical' THEN 3 WHEN 'warning' THEN 2 ELSE 1 END) AS level
        FROM latest l
        LEFT JOIN alert_rule r ON r.enabled AND r.metric_name = l.metric_name
            AND ((r.condition = 'gt' AND l.value > r.threshold)
              OR (r.condition = 'lt' AND l.value < r.threshold)
              OR (r.condition = 'eq' AND l.value = r.threshold))
        GROUP BY l.ci_id
    ), computed AS (
        SELECT c.id, COALESCE(CASE lv.level WHEN 3 THEN 'critical' WHEN 2 THEN 'warning' END,
                              CASE WHEN lv.ci_id IS NULL THEN 'unknown' ELSE 'ok' END) AS health
        FROM ci c LEFT JOIN level lv ON lv.ci_id = c.id
        WHERE c.deleted_at IS NULL
    ), updated AS (
        UPDATE ci SET health = computed.health, health_updated_at = now()
        FROM computed
        WHERE ci.id = computed.id AND ci.health IS DISTINCT FROM computed.health
        RETURNING 1
    )
    SELECT count(*) INTO changed FROM updated;
    PERFORM set_config('reticora.health_refresh', 'off', true);
    RETURN changed;
END
$$;
