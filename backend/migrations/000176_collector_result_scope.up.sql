-- Migration 000176: unmatched discovery results follow the client of their
-- collector (WP-636, TEN-02; F-363).
--
-- An unmatched discovery_result takes the client of its job's collector
-- only when it is written (derive_discovery_result_scope, 000061). When an
-- organization-wide collector was bound to a site of a client (trigger
-- collector_site_binding, 000127, sets its client), the results it produced
-- before kept client_id NULL and stayed readable for principals of every
-- client. A CI move propagates to its child rows (000141); for the
-- collector there was no such propagation.
--
-- propagate_collector_scope runs after every UPDATE of a collector whose
-- client_id changed (binding to a site, clearing the client, any other
-- path) and re-derives the unmatched results of its jobs in the same
-- statement; a matched result keeps the scope of its CI. Releasing the site
-- binding (site_id NULL) leaves the collector's client as it is
-- (collector_site_binding), so its results keep that client as well. Like
-- the propagation triggers of 000141 it runs with the client and site scope
-- widened (app_scope_widen/app_scope_restore, 000139): app.org_id stays, so
-- it reaches only rows of the collector's organization, and it returns no
-- rows to the caller. search_path is fixed. The policies of
-- discovery_result stay unchanged.
--
-- Backfill: existing unmatched results carry the client of their collector.

CREATE FUNCTION propagate_collector_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    -- A no-op UPDATE re-runs derive_discovery_result_scope.
    UPDATE discovery_result r SET job_id = r.job_id
      FROM discovery_job j
     WHERE j.id = r.job_id AND j.collector_id = NEW.id
       AND r.organization_id = NEW.organization_id AND r.matched_ci_id IS NULL;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_collector_result_scope AFTER UPDATE ON collector
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_collector_scope();

UPDATE discovery_result r SET job_id = r.job_id
  FROM discovery_job j JOIN collector col ON col.id = j.collector_id
 WHERE j.id = r.job_id AND r.matched_ci_id IS NULL
   AND (r.client_id IS DISTINCT FROM col.client_id OR r.site_id IS NOT NULL);
