-- WP-342 (TEN-02, TEN-05, TEN-06, TEN-09; F-003): review items carry the
-- client and site of what they are about.
--
-- Migration 000061 left review_item out: its policy checks the organization
-- only, and the repository's "all candidates visible" predicate is vacuously
-- true for an item without candidate CIs. A principal restricted to client 1
-- therefore listed, read and dismissed the unclassified_device,
-- unlicensed_ci and scope_suggested items about devices, collectors and
-- subnets of client 2.
--
-- 1. client_id and site_id (foreign keys with the organization). A BEFORE
--    trigger derives them: with candidates from the candidate CIs (their
--    common client and site, otherwise NULL), without candidates it keeps the
--    value the producer set only when it belongs to the organization (a site
--    decides its client). The derivation runs with the client and site scope
--    widened (app_scope_widen, 000139), so the writer's RLS does not hide
--    candidates (F-050).
-- 2. propagate_review_item_scope re-derives the items of a CI whose client or
--    site changes (WP-025: also after a move), likewise widened. The trigger
--    has no column list, like ci_scope_propagation: ci_derive_location
--    changes site_id without it being in the SET list.
-- 3. Backfill: items with candidates from the candidates, items without from
--    payload client_id/site_id or the collector of payload collector_id.
-- 4. Policies (TEN-05): the client and site predicate in USING (NULL stays
--    readable) and WITH CHECK (E-09: a scoped principal writes, and so
--    resolves, no item with client_id or site_id NULL). A restrictive policy
--    requires for a principal restricted in the client or site scope that
--    all candidate CIs are visible and that an item without candidates has a
--    client (and site) of the scope: an item without a derivable client is
--    visible to org-wide principals only (E-08, fail-closed). As for ci, a
--    restrictive DELETE policy requires the client and site of the scope.

-- ─── 1. Columns and derivation ────────────────────────────────────────────────

ALTER TABLE site ADD CONSTRAINT site_id_organization_id_key UNIQUE (id, organization_id);

ALTER TABLE review_item
    ADD COLUMN client_id UUID,
    ADD COLUMN site_id UUID,
    ADD CONSTRAINT review_item_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE SET NULL (client_id),
    ADD CONSTRAINT review_item_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE SET NULL (site_id);

CREATE INDEX idx_review_item_client ON review_item (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_review_item_candidates ON review_item USING gin (candidate_ci_ids);

CREATE FUNCTION derive_review_item_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    previous text[] := app_scope_widen();
    candidates UUID[] := COALESCE(NEW.candidate_ci_ids, '{}');
    site_client UUID;
BEGIN
    IF cardinality(candidates) > 0 THEN
        SELECT CASE WHEN count(*) = (SELECT count(DISTINCT x) FROM unnest(candidates) AS x)
                         AND count(c.client_id) = count(*) AND count(DISTINCT c.client_id) = 1
                    THEN (array_agg(c.client_id))[1] END,
               CASE WHEN count(*) = (SELECT count(DISTINCT x) FROM unnest(candidates) AS x)
                         AND count(c.site_id) = count(*) AND count(DISTINCT c.site_id) = 1
                    THEN (array_agg(c.site_id))[1] END
          INTO NEW.client_id, NEW.site_id
          FROM ci c
         WHERE c.id = ANY (candidates) AND c.organization_id = NEW.organization_id;
    ELSE
        IF NEW.client_id IS NOT NULL
           AND NOT EXISTS (SELECT 1 FROM client WHERE id = NEW.client_id AND organization_id = NEW.organization_id) THEN
            NEW.client_id := NULL;
        END IF;
        IF NEW.site_id IS NOT NULL THEN
            SELECT s.client_id INTO site_client FROM site s WHERE s.id = NEW.site_id AND s.organization_id = NEW.organization_id;
            IF site_client IS NULL OR site_client IS DISTINCT FROM COALESCE(NEW.client_id, site_client) THEN
                NEW.site_id := NULL;
            ELSE
                NEW.client_id := site_client;
            END IF;
        END IF;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

CREATE TRIGGER review_item_scope BEFORE INSERT OR UPDATE ON review_item
    FOR EACH ROW EXECUTE FUNCTION derive_review_item_scope();

-- ─── 2. Propagation ────────────────────────────────────────────────────────────

-- A no-op UPDATE re-runs the derivation of every item with the CI as a
-- candidate.
CREATE FUNCTION propagate_review_item_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE review_item SET candidate_ci_ids = candidate_ci_ids
     WHERE organization_id = NEW.organization_id AND candidate_ci_ids @> ARRAY[NEW.id];
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER ci_review_item_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_review_item_scope();

-- ─── 3. Backfill ───────────────────────────────────────────────────────────────

-- updated_at stays: the backfill changes no content.
ALTER TABLE review_item DISABLE TRIGGER trg_review_item_updated_at;

UPDATE review_item SET candidate_ci_ids = candidate_ci_ids WHERE cardinality(COALESCE(candidate_ci_ids, '{}')) > 0;

UPDATE review_item r
   SET client_id = COALESCE(
           CASE WHEN r.payload ->> 'client_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                THEN (r.payload ->> 'client_id')::uuid END,
           (SELECT col.client_id FROM collector col
             WHERE r.payload ->> 'collector_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
               AND col.id = (r.payload ->> 'collector_id')::uuid AND col.organization_id = r.organization_id)),
       site_id = COALESCE(
           CASE WHEN r.payload ->> 'site_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                THEN (r.payload ->> 'site_id')::uuid END,
           (SELECT col.site_id FROM collector col
             WHERE r.payload ->> 'collector_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
               AND col.id = (r.payload ->> 'collector_id')::uuid AND col.organization_id = r.organization_id))
 WHERE cardinality(COALESCE(r.candidate_ci_ids, '{}')) = 0;

ALTER TABLE review_item ENABLE TRIGGER trg_review_item_updated_at;

-- ─── 4. Policies ───────────────────────────────────────────────────────────────

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    client_in CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_in CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    candidates CONSTANT text :=
        '(CASE WHEN cardinality(COALESCE(candidate_ci_ids, ''{}''::uuid[])) = 0 '
        'THEN (%1$s OR client_id IS NOT NULL) AND (%2$s OR site_id IS NOT NULL) '
        'ELSE NOT EXISTS (SELECT 1 FROM unnest(candidate_ci_ids) AS candidate(id) '
        'WHERE NOT EXISTS (SELECT 1 FROM ci WHERE ci.id = candidate.id)) END)';
    scoped text;
BEGIN
    DROP POLICY review_item_isolation ON review_item;
    EXECUTE format(
        'CREATE POLICY review_item_isolation ON review_item '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR %3$s) AND (%4$s OR site_id IS NULL OR %5$s)) '
        'WITH CHECK (%1$s AND (%2$s OR %3$s) AND (%4$s OR %5$s))',
        org_match, client_all, client_in, site_all, site_in);
    scoped := format('%s AND ((%s AND %s) OR %s)', org_match, client_all, site_all, format(candidates, client_all, site_all));
    EXECUTE format('CREATE POLICY review_item_scoped_select ON review_item AS RESTRICTIVE FOR SELECT USING (%s)', scoped);
    EXECUTE format('CREATE POLICY review_item_scoped_update ON review_item AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   scoped, scoped);
    EXECUTE format('CREATE POLICY review_item_scoped_delete ON review_item AS RESTRICTIVE FOR DELETE '
                   'USING (%1$s AND (%2$s OR %3$s) AND (%4$s OR %5$s))',
                   org_match, client_all, client_in, site_all, site_in);
END
$$;
