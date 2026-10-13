-- Migration 000131: CI merge with tombstone and snapshot (CI-12, WP-173).
--
-- POST /cis/{survivor}/merge moves the references of the victim to the
-- survivor and soft-deletes the victim (deleted_at, merged_into_id). One
-- row per merge keeps:
--   * the tombstone: GET on the victim answers 301 to the survivor for
--     12 months (tombstone_until), also after the hard delete of the victim
--     row, so the ids are no foreign keys;
--   * the snapshot for the unmerge within 30 days (snapshot_until): both
--     CI rows before the merge, the moved reference rows and the rows the
--     merge removed (duplicates on the survivor).
-- The client and site of the survivor scope the row like the CI.

CREATE TABLE ci_merge (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID REFERENCES client (id) ON DELETE CASCADE,
    site_id         UUID REFERENCES site (id) ON DELETE SET NULL,
    survivor_id     UUID NOT NULL,
    victim_id       UUID NOT NULL,
    job_id          UUID,
    actor_id        UUID,
    snapshot        JSONB NOT NULL,
    merged_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    tombstone_until TIMESTAMPTZ NOT NULL DEFAULT now() + interval '12 months',
    snapshot_until  TIMESTAMPTZ NOT NULL DEFAULT now() + interval '30 days',
    unmerged_at     TIMESTAMPTZ,
    CONSTRAINT ci_merge_not_self CHECK (survivor_id <> victim_id)
);
-- A victim is merged at most once at a time.
CREATE UNIQUE INDEX uq_ci_merge_victim_active ON ci_merge (organization_id, victim_id) WHERE unmerged_at IS NULL;
CREATE INDEX idx_ci_merge_survivor ON ci_merge (organization_id, survivor_id);
CREATE INDEX idx_ci_merge_client ON ci_merge (client_id) WHERE client_id IS NOT NULL;

ALTER TABLE ci_merge ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_merge FORCE ROW LEVEL SECURITY;
CREATE POLICY ci_merge_isolation ON ci_merge FOR ALL
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY ci_merge_scoped_update ON ci_merge AS RESTRICTIVE FOR UPDATE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY ci_merge_scoped_delete ON ci_merge AS RESTRICTIVE FOR DELETE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
